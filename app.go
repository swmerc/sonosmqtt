package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	log "github.com/sirupsen/logrus"
	sonos "github.com/swmerc/sonosmqtt/sonos"
)

type appState int

const (
	Idle appState = iota
	Searching
	Polling
	CreateWebsockets
	Subscribe
	Listen
)

type SonosResponseWithId struct {
	playerId string
	sonos.WebsocketResponse
}

type ErrorWithId struct {
	playerId string
	error
}

// App contains all global state.  Ew.  Needs an interface?
type App struct {
	config     Config
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
	responseChannel chan SonosResponseWithId
	errorChannel    chan ErrorWithId

	// Groups is a map of every group indexed by PlayerId of the coordinator, and groupsSource
	// is the PlayerId of the player we subscribed to the groups namespace on.  It is a little
	// special since we need to switch it if that websocket bounces.
	groupsLock   sync.RWMutex
	groups       map[string]Group
	groupsSource string

	// New map of groups to switch over to when we create websockets
	groupUpdate map[string]Group

	// Cache of data we sent over MQTT
	mqttCache map[string]bool
}

func NewApp(config Config, client mqtt.Client) *App {
	return &App{
		config:          config,
		mqttClient:      client,
		currentState:    Idle,
		responseChannel: make(chan SonosResponseWithId),
		errorChannel:    make(chan ErrorWithId),
		groups:          map[string]Group{},
		groupsSource:    "",
		groupUpdate:     map[string]Group{},
		mqttCache:       map[string]bool{},
	}
}

func (app *App) run() {

	lastState := app.currentState

	//
	// Spin forever, because we have nothing better to do
	//
	for {

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
				var response sonos.GroupsResponse

				log.Infof("found: %s", player.String())
				if response, err = app.getGroupsRest(player); err == nil {
					if app.groupUpdate, err = getGroupMap(player.GetHouseholdId(), response); err == nil {
						app.currentState = CreateWebsockets
					}
				}
			}

			if err != nil {
				log.Errorf("Search error: ", err.Error())
				time.Sleep(time.Second * 10)
			}

		case CreateWebsockets:
			// Close the old websockets.
			// CODEME: I need an easier way to iterate over all players
			for _, group := range app.groups {
				for _, player := range group.Players {
					player.CloseWebsocketConnection()
				}
			}

			// Prepare to switch over to the new group list
			app.groupsLock.Lock()
			app.groups = app.groupUpdate
			app.groupsLock.Unlock()

			app.groupUpdate = nil

			// Empty channels now that the websocket is down and not generating new events
			for len(app.errorChannel) > 0 {
				<-app.errorChannel
			}
			for len(app.responseChannel) > 0 {
				<-app.responseChannel
			}

			//
			// Create websockets and hook up the callbacks
			//
			httpHeaders := http.Header{}
			app.addApiKey(&httpHeaders)

			first := true

			// CODEME: I need an easier way to iterate over all players
			for _, group := range app.groups {
				for _, player := range group.Players {

					if err := player.InitWebsocketConnection(httpHeaders, app); err != nil {
						log.Errorf("app: Unable to open websocket for %s: %s", player.GetId(), err.Error())
						continue
					}

					// Only subscribe to groups on one player.  It does not need to be a coordinator
					if first {
						first = false
						app.groupsSource = player.GetId()
						player.SendCommandViaWebsocket("groups", "subscribe", nil)
					}

					// Subscribe to the list of namespaces provided in the config file on
					// all group coordinators.  We probably want lists for:
					//
					// 1) Global stuff (in the first section above)
					// 2) Stuff for all group coordinators
					// 3) Stuff for all players (networking status, whatever)
					if group.Coordinator.GetId() == player.GetId() {
						for _, namespace := range app.config.Sonos.Subscriptions.Group {
							player.SendCommandViaWebsocket(namespace, "subscribe", nil)
						}
					}
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

// handleResponse is run on the main goroutine so it can muck with the state machine. Yup,
// the entire state machine needs to go, and this should simply return a new groupsMap if
// we have one instead of kicking the state machine here.
func (app *App) handleResponse(msg SonosResponseWithId) {

	// Handle subscription responses
	if msg.Headers.Response == "subscribe" {
		log.Debugf("app: subscribed to %s: %s", msg.Headers.Namespace, msg.playerId)
		return
	}

	// Look up the group
	group, ok := app.groups[msg.playerId]
	if !ok {
		log.Errorf("app: handleResponse: unknown player: %s", msg.playerId)
		return
	}

	// FIXME: Filter out errors here?
	if msg.Headers.Type == "none" || msg.Headers.Type == "globalError" {
		log.Infof("msg: %v", msg)
		return
	}

	//
	// Process the ones we care about.  Only one for now.
	//
	publishPlayers := false
	if msg.Headers.Type == "groups" {

		// Make sure we can parse it
		groupsResponse := sonos.GroupsResponse{}
		if err := json.Unmarshal(msg.BodyJSON, &groupsResponse); err != nil {
			return
		}

		player := group.Coordinator
		log.Infof("app: groups event: player=%s", player.GetName())

		// If the list of groups is different, kick the main state machine so we can connect to all of the correct players
		if groups, err := getGroupMap(player.GetHouseholdId(), groupsResponse); err == nil {
			if !groupsAreCloseEnoughForMe(app.groups, groups) {
				// This line is insanely slow...
				app.RemoveStaleTopics(missingPlayers(app.groups, groups), missingGroups(app.groups, groups))

				app.groupUpdate = groups
				app.currentState = CreateWebsockets
			}
		}

		// Always publish the players when we publish the groups
		publishPlayers = true
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

		// Simplify?
		if app.config.Sonos.Simplify {
			simplifySonosType(&msg)
		}

		app.PublishEventToAllTopics(group, &msg)

		// Publish players if needed.  A little tricky the first time around since we
		// always get an event even though we grabbed the groups via REST before
		// we subscribed.  This means that the first time through the right map
		// is app.groups.
		//
		// I really need to refactor all of this and dump app.groupUpdate entirely.
		if publishPlayers {
			groups := &app.groups
			if len(app.groupUpdate) != 0 {
				groups = &app.groupUpdate
			}
			hhPath := fmt.Sprintf("%s/%s", app.config.MQTT.Topic, "players")
			bytes, _ := getPlayersJSONFromGroupMap(*groups)
			app.PublishEventToTopic(hhPath, bytes)
		}
	}
}

func (app *App) PublishEventToAllTopics(group Group, msg *SonosResponseWithId) {

	// Paths
	//
	// Household events:
	//   {app.config.MQTT.Topic}/v1/events/{msg.Headers.Type}
	//
	// Group events:
	//   Fanout disabled:
	//     {app.config.MQTT.Topic}/v1/events/group/{coordinatorId}/{msg.Headers.Type}
	//   Fanout enabled:
	//     {app.config.MQTT.Topic}/v1/events/player/{playerIdForEachPlayerInGroup}/{msg.Headers.Type}
	//
	// Player events (eventually):
	//     {app.config.MQTT.Topic}/v1/events/player/{playerId}/{msg.Headers.Type}
	//
	// NOTE: This currently assumes that namespace does not really matter for events.  More
	//       specifically that there are no Types with the same name in different namespaces
	//       unless they are really the same Type.  Probably a bad assumption, but it cleans
	//       up the paths a bit.  We can always add {msg.Headers.Namespace} back in the path
	//       if we care.
	if msg.Headers.GroupId == "" {
		hhPath := fmt.Sprintf("%s/%s", app.config.MQTT.Topic, msg.Headers.Type)
		app.PublishEventToTopic(hhPath, msg.BodyJSON)
	} else {
		groupPath := fmt.Sprintf("%s/group/%s/%s", app.config.MQTT.Topic, group.Coordinator.GetId(), msg.Headers.Type)
		app.PublishEventToTopic(groupPath, msg.BodyJSON)
		if app.config.Sonos.FanOut {
			for _, player := range group.Players {
				playerPath := fmt.Sprintf("%s/player/%s/%s", app.config.MQTT.Topic, player.GetId(), msg.Headers.Type)
				app.PublishEventToTopic(playerPath, msg.BodyJSON)
			}
		}
	}
}

// PublishEventToTopic publishes a byte slice to a single MQTT topic.  It also keeps track of the topics
// we have published to so we can clear them later as needed.
func (app *App) PublishEventToTopic(topic string, body []byte) {

	// Stash it.  Memory is cheap.
	app.mqttCache[topic] = true

	// Publish
	//
	// NOTE: We currently send this at a QoS of 1 and retain.  Retaining is a pain, and in part why we
	//       have the cache.  If we dump retain and add a method for refreshing the content when a new
	//       device connects (likely via the device eventing), we can skip retain.  The downside is that
	//       every subscriber will get a full data dump when a new subscriber is added.
	// log.Debugf("app: cache miss: %s", topic)
	app.mqttClient.Publish(topic, 1, true, body)
}

//
func (app *App) RemoveStaleTopics(players []string, groups []string) {
	var prefixes []string = make([]string, 0, 32)

	for _, player := range players {
		prefixes = append(prefixes, fmt.Sprintf("%s/v1/events/player/%s", app.config.MQTT.Topic, player))
	}

	for _, group := range groups {
		prefixes = append(prefixes, fmt.Sprintf("%s/v1/events/group/%s", app.config.MQTT.Topic, group))
	}

	log.Infof("app: prefixes: %s", strings.Join(prefixes, ","))
	for topic := range app.mqttCache {
		for _, prefix := range prefixes {
			if strings.HasPrefix(topic, prefix) {
				log.Infof("app: clearing %s", topic)
				delete(app.mqttCache, topic)
				app.mqttClient.Publish(topic, 1, false, "")
				break
			}
		}
	}
}

//
// All of On* callbacks are run in the websocket's goroutines
//

// OnError is called when a websocket error has occurred.  This is run in a goroutine
// owned by the websocket.
func (app *App) OnError(id string, err error) {
	app.errorChannel <- ErrorWithId{
		playerId: id,
		error:    err,
	}
}

// OnMessage is called when a message is received from a websocket.  This is run in
// a goroutine owned by the websocket.
func (app *App) OnEvent(id string, response sonos.WebsocketResponse) {
	app.responseChannel <- SonosResponseWithId{
		playerId:          id,
		WebsocketResponse: response,
	}
}

//
// Player stuff
//

func (app *App) discoverPlayer() Player {
	//
	// Iterate over the mDNS responses and fill in a list of all players.  It turns out that we only
	// need a single player in the correct HH to respond, so we may be able to do this as the mDNS
	// responses come in at some point.
	//
	// The loop is a bit funky since we currently can reject players if something goes wrong.  All errors
	// are continues so we try the next player in the array
	//
	for _, mdnsDevice := range sonos.ScanForPlayersViaMDNS(app.config.Sonos.ScanTime) {

		hhid, err := mdnsDevice.GetHouseholdId()
		if err != nil {
			log.Errorf("app: %s", err.Error())
			continue
		}

		// If we are looking for a specific HHID, skip players in different HHs.  If not,
		// we latch the first HHID we see and skip players from other HHs.  I suspect the
		// final variant will report data for all HHs, but I'm sticking with tracking
		// a single player in a single HH for now.
		if len(app.config.Sonos.HouseholdId) != 0 && hhid != app.config.Sonos.HouseholdId {
			log.Debugf("HHID filtered: %s", hhid)
			continue
		}

		infoUrl, err := mdnsDevice.GetInfoUrl()
		if err != nil {
			log.Errorf("app: %s", err.Error())
			continue
		}

		// New player. Hit /info to get the player data
		body, err := app.doRESTWithApiKey(infoUrl, http.MethodGet, nil)
		if err != nil {
			log.Errorf("app: %s", err.Error())
			continue
		}

		// Parse it and return our happy player so the caller can hit /groups
		var info sonos.PlayerInfoResponse
		log.Debugf("PlayerInfo: %s", string(body))
		if json.Unmarshal(body, &info) != nil {
			log.Errorf("Unable to parse response from /info")
		}

		return NewInternalPlayerFromInfoResponse(info)
	}

	// Did not find anything at all.  Weeee.
	return nil
}

//
// We get groups via REST at startup.  I could open a websocket on a random
// player, get the groups via that, close it, and open a websocket on the
// final player but it seems silly.  We need REST for GetInfo anyway.
//
func (app *App) getGroupsRest(p Player) (sonos.GroupsResponse, error) {
	raw, err := app.playerDoGET(p, "/groups")

	if err != nil {
		return sonos.GroupsResponse{}, err
	}

	var groups sonos.GroupsResponse
	if json.Unmarshal(raw, &groups) != nil {
		return sonos.GroupsResponse{}, err
	}

	return groups, nil
}

func (a *App) addApiKey(header *http.Header) {
	header.Add("X-Sonos-Api-Key", a.config.Sonos.ApiKey)
}

//
// Sonos REST support.  Note that this is in App since it needs the api key from the config.  Ew?
//
// I could split it out into another class and pass in the key at init time, I suppose.
//
func (a *App) doRESTWithApiKey(fullUrl string, method string, body []byte) ([]byte, error) {
	// FIXME: Can we just fix the CN, or are there really self signed?
	customTransport := http.DefaultTransport.(*http.Transport).Clone()
	customTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	client := &http.Client{Transport: customTransport}

	log.Debugf("REST: %s URL=%s", method, fullUrl)

	request, err := http.NewRequest(method, fullUrl, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	a.addApiKey(&request.Header)
	request.Header.Add("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		log.Errorf("REST: Do: %s", err.Error())
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		log.Errorf("REST: StatusCode: %d", response.StatusCode)
		return nil, fmt.Errorf("code: %d", response.StatusCode)
	}

	data, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (a *App) playerDoGET(p Player, path string) ([]byte, error) {
	return a.doRESTWithApiKey(p.CreateFullRESTUrl(path), http.MethodGet, nil)
}

func (a *App) playerDoPOST(p Player, path string, body []byte) ([]byte, error) {
	return a.doRESTWithApiKey(p.CreateFullRESTUrl(path), http.MethodPost, body)
}

//
// Data munging
//
func getPlayersJSONFromGroupMap(groups map[string]Group) ([]byte, error) {
	// Convert to an array since the map is useless to the end users.  Ew.
	var playerArray []Player = make([]Player, 0, 64)
	for _, g := range groups {
		for _, p := range g.Players {
			playerArray = append(playerArray, p)
		}
	}

	// Send it out.  Does this even work?
	bytes, err := json.Marshal(playerArray)
	return bytes, err
}
