package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	sonos "github.com/swmerc/sonosmqtt/sonos"
)

type PlayerEventHandler interface {
	OnEvent(playerId string, data []byte)
	OnError(playerId string, err error)
}

type Player interface {
	GetId() string

	GetHouseholdId() string
	GetGroupId() string
	GetName() string

	String() string

	SetCoordinator(coordinator Player, groupId string)

	// Temporary until we get real REST support
	CreateFullRESTUrl(subpath string) string

	// Websockets
	InitWebsocketConnection(headers http.Header, eventHandler PlayerEventHandler) error
	CloseWebsocketConnection()
	SendCommandViaWebsocket(namespace string, command string) error
}

type playerImpl struct {
	// Stuff from /info or /groups.  Not quite static, but we'll regenerate if GroupId changes so this is
	// all static enough for our purposes.
	//
	// We send a subset of this out over REST when asked, but the rest is internal.  The callers don't need
	// the Urls, for example, because they do not need them to talk to the players.  We handle all of that
	// on their behalf.
	//
	// Heck, callers don't even need CoordinatorId since they can use the id of any player in the group
	// to do group things.
	PlayerId      string `json:"id"`
	Name          string `json:"name"`
	groupId       string
	coordinatorId string
	householdId   string
	restUrl       string
	websocketUrl  string

	// websocket to the player.  All players we are tracking will have one.
	sync.RWMutex
	websocket    WebsocketClient
	eventHandler PlayerEventHandler
	cmdId        int32
}

//
// Functions to generate all of the data we need to talk to a player from a couple of sources.  I suppose
// I could just use one of the existing structs from Sonos responses and add in what I need.
//

// NewInternalPlayerFromInfoResponse takes the data returned from /info and turns it into
// our internal format.  Stuff not included is generated.
func NewInternalPlayerFromInfoResponse(info sonos.PlayerInfoResponse) Player {
	return &playerImpl{
		Name:          info.Device.Name,
		PlayerId:      info.PlayerId,
		groupId:       info.GroupId,
		coordinatorId: groupIdToCoordinatorId(info.GroupId),
		householdId:   info.HouseholdId,
		restUrl:       info.RestUrl,
		websocketUrl:  info.WebsocketUrl,
		cmdId:         1,
		websocket:     nil,
	}
}

// NewInternalPlayerFromSonos takes the player data returned in things like GroupsResponse and
// turns it into our internal format.  No, this is not the same data we get from /info, or at
// least it was not at some point.  The RestUrl may be included now.
func NewInternalPlayerFromSonosPlayer(player sonos.Player, householdId string, groupId string) Player {
	return &playerImpl{
		Name:          player.Name,
		PlayerId:      player.Id,
		groupId:       groupId,
		coordinatorId: groupIdToCoordinatorId(groupId),
		householdId:   householdId,
		restUrl:       restUrlFromWebsocketUrl(player.WebsocketUrl),
		websocketUrl:  player.WebsocketUrl,
		cmdId:         1,
		websocket:     nil,
	}
}

//
// Functions to cheat and create data that the API doesn't provide at the time it is needed
//
func groupIdToCoordinatorId(groupId string) string {
	last := strings.LastIndex(groupId, ":")

	if last > 0 {
		return groupId[:last]
	}

	return groupId
}

func restUrlFromWebsocketUrl(websocketUrl string) string {
	restUrl := strings.Replace(websocketUrl, "wss", "https", -1)
	return strings.Replace(restUrl, "/websocket", "", -1)
}

//
// Stuff to support the interface
//

func (p *playerImpl) GetId() string {
	return p.PlayerId
}

func (p *playerImpl) GetName() string {
	return p.Name
}

func (p *playerImpl) GetHouseholdId() string {
	return p.householdId
}

func (p *playerImpl) GetGroupId() string {
	return p.groupId
}

func (p *playerImpl) String() string {
	return fmt.Sprintf("name=%s, id=%s, groupid=%s, wsurl=%s, resturl=%s", p.Name, p.PlayerId, p.groupId, p.websocketUrl, p.restUrl)
}

func (p *playerImpl) CreateFullRESTUrl(subpath string) string {
	// Yup, we assume V1 and local HH.  No idea why the LAN variant has multi HH support when the
	// players do not.  Unless it is to match the cloud API, but the "local" bit makes it not match
	// anyway.
	//
	// NOTE: We should move the code that talks to players in here and hide all of the Urls
	return fmt.Sprintf("%s/v1/households/local%s", p.restUrl, subpath)
}

func (p *playerImpl) SetCoordinator(coordinator Player, groupId string) {
	p.coordinatorId = coordinator.GetId()
	p.groupId = groupId
}

//
// Player websockets.  This will likely get split out.
//

func (p *playerImpl) InitWebsocketConnection(headers http.Header, eventHandler PlayerEventHandler) error {
	// We only ever create websockets on one thread, but we currently remove them in OnClose() below
	// so we need locking.  Weee.
	p.RLock()
	if p.websocket != nil {
		p.RUnlock()
		return nil
	}
	p.RUnlock()

	// We point the callbacks to this object, which passes along things of interest to the
	// event handler (which only contains events).  We'll likely have to add a Close handler
	// so we can reach to players going away.
	ws := NewClientWebSocket(p.websocketUrl, p.PlayerId, headers, p)

	if ws != nil {
		p.Lock()
		p.eventHandler = eventHandler
		p.websocket = ws
		p.Unlock()
	}

	if ws == nil {
		return fmt.Errorf("unable to create websocket for %s", p.PlayerId)
	}

	return nil
}

func (p *playerImpl) CloseWebsocketConnection() {
	p.Lock()
	defer p.Unlock()

	if p.eventHandler != nil {
		p.eventHandler = nil
	}

	if p.websocket != nil {
		p.websocket.Close()
	}
}

func (p *playerImpl) SendCommandViaWebsocket(namespace string, command string) error {
	// Lock for the bits that touch cmdId and grab a reference
	p.Lock()

	// Grab a reference to the websocket.  We're sending this no matter what
	ws := p.websocket
	if ws == nil {
		p.Unlock()
		return fmt.Errorf("player: %s: attempt to send with no websocket", p.PlayerId)
	}

	request := &sonos.WebsocketRequest{
		Headers: sonos.RequestHeaders{
			CommonHeaders: sonos.CommonHeaders{
				Namespace:   namespace,
				Command:     command,
				UserId:      "",
				HouseholdId: p.householdId,
				GroupId:     p.groupId,
				PlayerId:    p.PlayerId,
				CmdId:       fmt.Sprintf("%d", p.cmdId),
				Topic:       "",
			},
		},
		BodyJSON: []byte{},
	}

	p.cmdId = p.cmdId + 1

	p.Unlock()

	// Might as well convert to JSON, log, and send outside of the lock
	msg, err := request.ToRawBytes()
	if err != nil {
		return err
	}

	log.Infof("player: ws: outgoing: %s", string(msg))

	return ws.SendMessage(msg)
}

//
// WebsocketCallbacks interface so we can get callbacks here.
//

func (p *playerImpl) OnConnect(userData string) {
	log.Infof("player: %s: connected", p.PlayerId)
}

func (p *playerImpl) OnError(userData string, err error) {
	p.RLock()
	eventHandler := p.eventHandler
	p.RUnlock()

	log.Infof("player: %s: error: %s", p.PlayerId, err.Error())
	if eventHandler != nil {
		eventHandler.OnError(userData, err)
	}
}

func (p *playerImpl) OnClose(userData string) {
	p.Lock()
	defer p.Unlock()

	p.websocket = nil
	p.eventHandler = nil
}

func (p *playerImpl) OnMessage(userData string, msg []byte) {
	p.RLock()
	eventHandler := p.eventHandler
	p.RUnlock()

	// CODEME: All go to the event handler for now, but we only want actual events to
	//         go at some point.
	if eventHandler != nil {
		eventHandler.OnEvent(userData, msg)
	}
}
