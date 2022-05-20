package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	log "github.com/sirupsen/logrus"
)

// AppConfig defines the server options we support in the config file.  Who knew?
type AppConfig struct {
	// Log level
	Debug bool `yaml:"debug"`

	// Sonos options
	Sonos struct {
		ApiKey      string `yaml:"apikey"`
		HouseholdId string `yaml:"household"` // Filter to households with this if provided

		// Things to subscribe to, and how to handle the data
		Subscriptions []string `yaml:"subscriptions"`
		Simplify      bool     `yaml:"simplify"`

		// Geekier stuff.  May go away.
		ScanTime uint `yaml:"scantime"` // Time to wait for mDNS responses.  Defaults to 5 seconds.
		FanOut   bool `yaml:"fanout"`   // True to copy coordinator events to players
	} `yaml:"sonos"`

	// MQTT broker-isms
	MQTT struct {
		Config MQTTConfig `yaml:"broker"`
		Topic  string     `yaml:"topic"`
	} `yaml:"mqtt"`
}

type appState int

const (
	Idle appState = iota
	Searching
	Polling
	CreateWebsockets
	Subscribe
	Listen
)

type MuseResponseWithId struct {
	playerId string
	MuseResponse
}

type ErrorWithId struct {
	playerId string
	error
}

// App contains all global state.  Ew.  Needs an interface?
type App struct {
	config     AppConfig
	mqttClient mqtt.Client

	// Current state
	currentState appState

	// Channels to deal with data from the websocket
	//
	// NOTE: Having a channel per message type does not scale well, but neither
	//       does having one goroutine parse everything.  I suppose what I really
	//       need is a common base message type that holds a parsed message, but
	//       that also seems like a royal pain.
	//
	//       I'll parse it all in one goroutine for now.  I don't expect a ton of
	//       traffic anyway.
	responseChannel chan MuseResponseWithId
	errorChannel    chan ErrorWithId

	// Groups is a map of every group indexed by PlayerId of the coordinator, and groupsSource
	// is the PlayerId of the player we subscribed to the groups namespace on.  It is a little
	// special since we need to switch it if that websocket bounces.
	groups       map[string]Group
	groupsSource string

	// New map of groups to switch over to when we create websockets
	groupUpdate map[string]Group

	// Cache of data we sent over MQTT
	mqttCache map[string][]byte
}

func NewApp(config AppConfig, client mqtt.Client) *App {
	return &App{
		config:          config,
		mqttClient:      client,
		currentState:    Idle,
		responseChannel: make(chan MuseResponseWithId),
		errorChannel:    make(chan ErrorWithId),
		groups:          map[string]Group{},
		groupsSource:    "",
		groupUpdate:     map[string]Group{},
		mqttCache:       map[string][]byte{},
	}
}

func (app *App) run() {

	lastState := app.currentState

	//
	// Spin forever, because we have nothing better to do
	//
	for true {

		if lastState != app.currentState {
			log.Infof("app: state change: %d -> %d", lastState, app.currentState)
			lastState = app.currentState
		}

		switch app.currentState {
		case Idle:
			app.currentState = Searching

		case Searching:
			var err error = fmt.Errorf("timeout")

			if player := app.discoverPlayer(); player != nil {
				var response MuseGroupsResponse

				log.Infof("found: %s", player.String())
				if response, err = app.getGroupsRest(*player); err == nil {
					if app.groupUpdate, err = getGroupMap(player.HouseholdId, response); err == nil {
						app.currentState = CreateWebsockets
					}
				}
			}

			if err != nil {
				log.Errorf("Search error: ", err.Error())
				time.Sleep(time.Second * 10)
			}

		case CreateWebsockets:
			// Close the old websockets
			for _, group := range app.groups {
				if group.Coordinator.Websocket != nil {
					log.Infof("app: closing websocket for %s", group.Coordinator.PlayerId)
					group.Coordinator.Websocket.Close()
					group.Coordinator.Websocket = nil
				}
			}

			// Prepare to switch over to the new group list
			app.groups = app.groupUpdate
			app.groupUpdate = nil

			// Empty channels now that the websocket is down and not generating new events
			for len(app.errorChannel) > 0 {
				<-app.errorChannel
			}
			for len(app.responseChannel) > 0 {
				<-app.responseChannel
			}

			// Clear last metadata so the next one is seen as new
			// app.lastMetadata = MusePlaybackExtendedResponse{}

			//
			// Create websockets and hook up the callbacks
			//
			httpHeaders := http.Header{}
			app.addApiKey(&httpHeaders)

			first := true

			for _, group := range app.groups {
				player := group.Coordinator
				log.Infof("app: connecting to %s, AKA %s", player.Name, player.PlayerId)
				player.Websocket = NewWebSocket(player.WebsocketUrl, player.PlayerId, httpHeaders, app)

				if player.Websocket == nil {
					log.Errorf("app: Unable to open websocket")
					continue
				}

				// Only subscribe to groups on one player
				if first {
					first = false
					app.groupsSource = player.PlayerId
					app.SendMessageToPlayer(player, "groups", "subscribe")
				}

				// Subscribe to the list of namespaces provided in the config file on
				// all GCs (for now).  We probably want lists for:
				//
				// 1) Global stuff (in the first section above)
				// 2) Stuff for all GCs
				// 3) Stuff for all players (networking status, whatever)
				for _, namespace := range app.config.Sonos.Subscriptions {
					app.SendMessageToPlayer(player, namespace, "subscribe")
				}
			}

			app.currentState = Listen

		case Listen:
			for {
				select {
				case msg := <-app.responseChannel:
					app.handleResponse(msg)
				case err := <-app.errorChannel:
					log.Debugf("app: ws error=%s", err.Error())
					app.currentState = Idle
				}
				if app.currentState != Listen {
					break
				}
			}
		}
	}
}

func (app *App) handleResponse(msg MuseResponseWithId) {
	// Handle subscription responses
	if msg.Headers.Response == "subscribe" {
		log.Debugf("app: subscribed to %s: %s", msg.Headers.Namespace, msg.playerId)
		return
	}

	// Look up the group
	group, ok := app.groups[msg.playerId]
	if ok == false {
		log.Errorf("app: handleResponse: unknown player: %s", msg.playerId)
		return
	}

	player := group.Coordinator

	// FIXME: Filter out errors here?

	//
	// Process the ones we care about.  Only one for now.
	//
	if msg.Headers.Type == "groups" {
		// Make sure we can parse it
		groupsResponse := MuseGroupsResponse{}
		if err := json.Unmarshal(msg.BodyJSON, &groupsResponse); err != nil {
			return
		}

		log.Infof("app: groups event: player=%s", player.Name)

		// If the list of groups is different, kick the main state machine so we can connect to all of the correct players
		if groups, err := getGroupMap(player.HouseholdId, groupsResponse); err == nil {
			if groupsAreCloseEnoughForMe(app.groups, groups) != true {
				app.groupUpdate = groups
				app.currentState = CreateWebsockets
				return
			}
		}
	}

	// Pretty sure we can blindly fan out any events have a groupid to the group?  I guess this means:
	//
	// 1) No groupId or playerId goes to HH
	// 2) GroupId gets fanned out to players
	// 3) PlayerId gets sent to the player
	//
	// This should mean that we can publish here and process below
	//
	// NOTE: We also need to cache the last thing we wrote and only update if the content
	//       has changed.  This will also help in cleaning up the topics later when groups
	//       change.
	log.Debugf("app: handleResponse: id=%s: namespace=%s, type=%s, hhid=%s, groupid=%s", msg.playerId, msg.Headers.Namespace, msg.Headers.Type, msg.Headers.HouseholdId, msg.Headers.GroupId)

	if app.mqttClient != nil {
		path := fmt.Sprintf("%s/%s", app.config.MQTT.Topic, player.HouseholdId)

		// Simplify?
		if app.config.Sonos.Simplify {
			simplifyMuseType(&msg)
		}

		// Fan it out?
		if msg.Headers.GroupId == "" {
			hhPath := fmt.Sprintf("%s/%s", path, msg.Headers.Type)
			app.PublishViaMQTT(hhPath, msg.BodyJSON)
		} else if app.config.Sonos.FanOut {
			for _, player := range group.Players {
				playerPath := fmt.Sprintf("%s/%s/%s", path, player.PlayerId, msg.Headers.Type)
				app.PublishViaMQTT(playerPath, msg.BodyJSON)
			}
		} else {
			groupPath := fmt.Sprintf("%s/%s/%s", path, group.Coordinator.GroupId, msg.Headers.Type)
			app.PublishViaMQTT(groupPath, msg.BodyJSON)
		}
	}

}

//
// All of On* callbacks are run in the websocket's goroutines
//
func (app *App) OnConnect(id string) {
	log.Infof("app: connected to %s", id)
}

func (app *App) OnError(id string, err error) {
	app.errorChannel <- ErrorWithId{
		playerId: id,
		error:    err,
	}
}

func (app *App) OnMessage(id string, data []byte) {
	// Parse the response
	var museResponse MuseResponse
	if err := museResponse.fromRawBytes(data); err != nil {
		log.Errorf("app: unable to parse: %s (%s)", err.Error(), string(data))
	}

	//log.Debugf("RX: Player: %s, Headers: %v, Body: %s", id, museResponse.Headers, museResponse.BodyJSON)

	app.responseChannel <- MuseResponseWithId{
		playerId:     id,
		MuseResponse: museResponse,
	}
}

func (app *App) OnClose(id string) {
	log.Infof("app: OnClose: %s", id)
}

//
// MQTT publishing all goes through here so I can check a cache.  Slow and unbounded memory usage.  WOOO.
//
func (app *App) PublishViaMQTT(topic string, body []byte) {
	// If this is an exact match, don't publish again
	if last, ok := app.mqttCache[topic]; ok {
		if bytes.Compare(body, last) == 0 {
			log.Debugf("app: cache hit:  %s", topic)
			return
		}
	}

	// Stash it.  Memory is cheap.
	app.mqttCache[topic] = body

	// Publish
	log.Infof("app: cache miss: %s", topic)
	app.mqttClient.Publish(topic, 1, true, body)
}

//
// Player stuff
//
func (app *App) SendMessageToPlayer(player *Player, namespace string, command string) error {
	cmdId := player.CmdId
	player.CmdId = player.CmdId + 1

	headers := &MuseHeaders{
		Namespace:   namespace,
		Command:     command,
		HouseholdId: player.HouseholdId,
		GroupId:     player.GroupId,
		CmdId:       fmt.Sprintf("%d", cmdId),
	}

	headersJSON, err := json.Marshal(headers)
	if err != nil {
		return err
	}

	//
	// NOTE: If we are going to rate limit, this is where it should happen.  It adds a pile of complexity,
	//       however, and we're not sending a ton of commands.
	//
	return player.Websocket.SendMessage([]byte(fmt.Sprintf("[%s,{}]", headersJSON)))
}

func (app *App) discoverPlayer() *Player {
	//
	// Iterate over the mDNS responses and fill in a list of all players.  It turns out that we only
	// need a single player in the correct HH to respond, so we may be able to do this as the mDNS
	// responses come in at some point.
	//
	// The loop is a bit funky since we currently can reject players if something goes wrong.  All errors
	// are continues so we try the next player in the array
	//
	for _, p := range scanForPlayers(app.config.Sonos.ScanTime) {

		// New player. Hit /info to get the player and /groups to get the rest of them
		body, err := app.museGetRestFromMDNSDevice(p, p.InfoUrl)
		if err != nil {
			log.Errorf("Unable to fetch info for %s (%s)", p.IP, err.Error())
			continue
		}

		var info MusePlayerInfoResponse
		log.Debugf("PlayerInfo: %s", string(body))
		if json.Unmarshal(body, &info) != nil {
			log.Errorf("Unable to parse response from /info")
		}

		// If we are looking for a specific HHID, skip players in different HHs.  If not,
		// we latch the first HHID we see and skip players from other HHs.  I suspect the
		// final variant will report data for all HHs, but I'm sticking with tracking
		// a single player in a single HH for now.
		thisPlayer := newInternalPlayerFromPlayerInfoReponse(info)
		if len(app.config.Sonos.HouseholdId) != 0 && stripMuseHouseholdId(thisPlayer.HouseholdId) != app.config.Sonos.HouseholdId {
			log.Debugf("HHID filtered: %s", thisPlayer.HouseholdId)
			continue
		}

		// Finally ready to admit that we like this player.  Return it.
		return thisPlayer
	}

	// Did not find anything at all.  Weeee.
	return nil
}

// Group contains the information required to turn group events into individual player events.
//
// The Coordinator is the player that is actually downloading/playing the content.  It sends
// this data to the other players.  Note that I left the cooridinator in the map of players
// to make it easier to iterate over at the expense of a little data.
//
// Note that we have a TON of copies of the PlayerId for now (and perhaps always).  Oh well.
type Group struct {
	Coordinator *Player
	Players     map[string]*Player
}

func groupsAreCloseEnoughForMe(a, b map[string]Group) bool {

	// Quick and dirty length check.
	if len(a) != len(b) {
		return false
	}

	// Number of groups matches, make sure each one matches
	for id, group := range a {

		// Miss
		groupMatch, ok := b[id]
		if ok != true {
			return false
		}

		// Hit with different number of players
		if len(group.Players) != len(groupMatch.Players) {
			return false
		}

		// Walk the players.  Almost done.
		for id := range group.Players {
			if _, ok := groupMatch.Players[id]; ok != true {
				return false
			}
		}

	}

	return true
}

// getGroupMap parses a MuseGroupsResponse and returns a map of all Groups indexed by PlayerId.
func getGroupMap(hhid string, groupsResponse MuseGroupsResponse) (map[string]Group, error) {
	var allPlayers map[string]*Player = make(map[string]*Player, 32)
	var allGroups map[string]Group = make(map[string]Group, 32)

	// Stash all of the players
	for _, p := range groupsResponse.Players {
		player := newInternalPlayerFromMusePlayer(p, hhid, "") // We don't know GroupId yet
		allPlayers[player.PlayerId] = player
	}

	// Process the groups and create them from the players
	for _, group := range groupsResponse.Groups {
		if coordinator, ok := allPlayers[group.CoordinatorId]; ok {

			// We now know groupId.  This is good because we need it for the Muse headers later.
			//
			// I could likely pull this out of Player and toss it into Group when we subscribe to all groups?
			coordinator.GroupId = group.Id

			players := make(map[string]*Player, 32)
			for _, playerId := range group.PlayerIds {
				if player, ok := allPlayers[playerId]; ok {
					player.GroupId = group.Id
					player.CoordinatorId = coordinator.PlayerId
					players[player.PlayerId] = player
				}
			}

			newGroup := Group{
				Coordinator: coordinator,
				Players:     players,
			}
			allGroups[coordinator.PlayerId] = newGroup
		}
	}

	return allGroups, nil
}

//
// We get groups via REST at startup.  I could open a websocket on a random
// player, get the groups via that, close it, and open a websocket on the
// final player but it seems silly.  We need REST for GetInfo anyway.
//
func (app *App) getGroupsRest(p Player) (MuseGroupsResponse, error) {
	raw, err := app.museGetRestFromPlayer(p, "households/local/groups")

	if err != nil {
		return MuseGroupsResponse{}, err
	}

	var groups MuseGroupsResponse
	if json.Unmarshal(raw, &groups) != nil {
		return MuseGroupsResponse{}, err
	}

	return groups, nil
}

func (a *App) addApiKey(header *http.Header) {
	header.Add("X-Sonos-Api-Key", a.config.Sonos.ApiKey)
}

//
// Muse REST support.  Note that this is in App since it needs the api key from the config.  Ew?
//
// I could split it out into another class and pass in the key at init time, I suppose.
//
func (a *App) museGetRest(fullUrl string) ([]byte, error) {
	// FIXME: Can we just fix the CN, or are there really self signed?
	customTransport := http.DefaultTransport.(*http.Transport).Clone()
	customTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	client := &http.Client{Transport: customTransport}

	request, err := http.NewRequest(http.MethodGet, fullUrl, nil)
	if err != nil {
		return nil, err
	}
	a.addApiKey(&request.Header)

	log.Debugf("REST: URL=%s", fullUrl)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bad HTTP status for %s: %d", fullUrl, response.StatusCode)
	}

	data, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	// log.Debugf("REST: resp: body=%s", string(data))

	return data, nil
}

func (a *App) museGetRestFromMDNSDevice(d MDNSDevice, path string) ([]byte, error) {
	return a.museGetRest(fmt.Sprintf("%s%s", d.BaseUrl, path))
}

func (a *App) museGetRestFromPlayer(p Player, path string) ([]byte, error) {
	return a.museGetRest(fmt.Sprintf("%s/v1/%s", p.RestUrl, path))
}
