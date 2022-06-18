package main

import (
	"encoding/json"
	"log"
	"net/http"
	"testing"
	"time"

	sonos "github.com/swmerc/sonosmqtt/sonos"
)

//
// Object creation, with optional websocket init
//
func newDefaultPlayer() Player {
	info := sonos.PlayerInfoResponse{
		Device: struct {
			Name string "json:\"name\""
		}{
			Name: "FooMatic",
		},
		HouseholdId:  "HHID",
		GroupId:      "GID:PORT",
		PlayerId:     "PID",
		WebsocketUrl: "WSURL",
		RestUrl:      "RESTURL",
	}
	return NewInternalPlayerFromInfoResponse(info)
}

//
// CheesyTestStuff ties all of the mocks together.  A ball of spaghetti still looks like a ball,
// right?
//
type CheesyTestStuff struct {
	t               *testing.T
	player          Player
	websocketClient *MockWebsocketClient
	eventHandler    *MockEventHandler
	responseChannel chan sonos.WebsocketResponse
	eventChannel    chan sonos.WebsocketResponse
}

func newCheesyTestStuff(t *testing.T) *CheesyTestStuff {
	// Reset the test hooks here
	playerCmdTimeout = 10 * time.Second

	cheese := CheesyTestStuff{
		t:               t,
		player:          newDefaultPlayer(),
		websocketClient: newMockWebsocketClient(),
		eventHandler:    newMockEventHandler(),
		responseChannel: make(chan sonos.WebsocketResponse, 16),
		eventChannel:    make(chan sonos.WebsocketResponse, 16),
	}

	// Plumb recevied events from the app-level event handler back to us
	cheese.eventHandler.SetEventChannel(cheese.eventChannel)

	if err := cheese.player.InitWebsocketConnection(http.Header{}, cheese.eventHandler); err != nil {
		t.Errorf("Unable to init websocket connection: %s", err.Error())
	}

	return &cheese
}

func (c *CheesyTestStuff) SendCommand(namespace string, command string) {
	if err := c.player.SendCommandViaWebsocket(namespace, command, func(resp sonos.WebsocketResponse) {
		c.responseChannel <- resp
	}); err != nil {
		c.t.Errorf("unable to send to player: %s", err.Error())
	}
}

func (c *CheesyTestStuff) InjectEvent(event sonos.WebsocketResponse) {
	raw, err := event.ToRawBytes()

	if err != nil {
		c.t.Errorf("error converting event to bytes: %s", err.Error())
	}

	c.websocketClient.callbacks.OnMessage(c.websocketClient.userData, raw)
}

func (c *CheesyTestStuff) GetResponse() sonos.WebsocketResponse {
	return (<-c.responseChannel)
}

func (c *CheesyTestStuff) GetEvent() sonos.WebsocketResponse {
	select {
	case response := <-c.eventChannel:
		return response
	case <-time.After(1 * time.Second):
		break
	}

	c.t.Error("timed out waiting for event")
	return sonos.WebsocketResponse{}
}

func (c *CheesyTestStuff) GetEventCount() uint32 {
	return c.eventHandler.count
}

func (c *CheesyTestStuff) CloseWebsocket() {
	c.websocketClient.Close()
}

func (c *CheesyTestStuff) SetCommandTimeout(timeout time.Duration, respond bool) {
	playerCmdTimeout = timeout
	c.websocketClient.respondToMessages = respond
}

//
// Websocket mock
//

// MockWebsocketClient implements the WebsocketClient interface
type MockWebsocketClient struct {
	// Data
	userData  string
	message   []byte
	callbacks WebsocketCallbacks
	closed    bool

	// Control
	respondToMessages bool
}

func newMockWebsocketClient() *MockWebsocketClient {
	// Fresh mocked websocket client
	client := &MockWebsocketClient{
		userData:          "",
		message:           []byte{},
		callbacks:         nil,
		closed:            true,
		respondToMessages: true,
	}

	// Override the test hook in player.go
	websocketInitHook = func(url string, userData string, headers http.Header, callbacks WebsocketCallbacks) WebsocketClient {
		client.userData = userData
		client.callbacks = callbacks
		return client
	}

	return client
}

func (ws *MockWebsocketClient) SendMessage(data []byte) error {

	request := sonos.WebsocketRequest{}
	if err := request.FromRawBytes(data); err != nil {
		log.Fatalf("can't parse request")
	}

	if ws.respondToMessages {
		// Loop it all back for now.  Should probably add something to it
		ws.message = data
		response := sonos.WebsocketResponse{
			Headers: sonos.ResponseHeaders{
				CommonHeaders: sonos.CommonHeaders{
					Namespace:   request.Headers.Namespace + "-resp",
					Command:     request.Headers.Command + "-resp",
					UserId:      request.Headers.UserId + "-resp",
					HouseholdId: request.Headers.HouseholdId + "-resp",
					GroupId:     request.Headers.GroupId + "-resp",
					PlayerId:    request.Headers.PlayerId + "-resp",
					CmdId:       request.Headers.CmdId,
					Topic:       request.Headers.Topic + "-resp",
				},
				Response: "Response, yo!",
				Success:  true,
				Type:     "none",
			},
			BodyJSON: []byte{},
		}
		body, _ := response.ToRawBytes()
		ws.callbacks.OnMessage(ws.userData, body)
	}

	return nil
}

func (ws *MockWebsocketClient) Close() {
	ws.closed = true
	ws.callbacks.OnClose(ws.userData)
}

func (ws *MockWebsocketClient) IsRunning() bool {
	return !ws.closed
}

func (ws *MockWebsocketClient) Error(err error) {
	ws.callbacks.OnError(ws.userData, err)
}

type MockEventHandler struct {
	count        uint32
	err          error
	eventChannel chan sonos.WebsocketResponse
}

func newMockEventHandler() *MockEventHandler {
	return &MockEventHandler{
		count:        0,
		err:          nil,
		eventChannel: nil,
	}
}

func (m *MockEventHandler) SetEventChannel(eventChannel chan sonos.WebsocketResponse) {
	m.eventChannel = eventChannel
}

func (m *MockEventHandler) OnEvent(playerId string, response sonos.WebsocketResponse) {
	m.count = m.count + 1
	if m.eventChannel != nil {
		m.eventChannel <- response
	}
}

func (m *MockEventHandler) OnError(playerId string, err error) {
	m.err = err
}

//
// Tests.  Finally.
//

func TestNewInternalPlayerFromInfoResponse(t *testing.T) {
	player := newDefaultPlayer()

	if hhid := player.GetHouseholdId(); hhid != "HHID" {
		t.Errorf("wrong HHID: %s instead of HHID", hhid)
	}

	if gid := player.GetGroupId(); gid != "GID:PORT" {
		t.Errorf("wrong GID: %s instead of GID", gid)
	}

	if pid := player.GetId(); pid != "PID" {
		t.Errorf("wrong PID: %s instead of PID", pid)
	}

	if name := player.GetName(); name != "FooMatic" {
		t.Errorf("wrong name: %s instead of FooMatic", name)
	}

	if url := player.CreateFullRESTUrl("/blah"); url != "RESTURL/v1/households/local/blah" {
		t.Errorf("wrong REST URL: %s instead of RESTURL/v1/households/local/blah", url)
	}
}

func TestNewInternalPlayerFromSonosPlayer(t *testing.T) {

	sonosPlayer := sonos.Player{
		Id:           "PID",
		Name:         "NAME",
		WebsocketUrl: "wss://WSURL/api/websocket",
		Capabilities: []string{},
	}

	player := NewInternalPlayerFromSonosPlayer(sonosPlayer, "HHID", "GID")

	if hhid := player.GetHouseholdId(); hhid != "HHID" {
		t.Errorf("wrong HHID: %s instead of HHID", hhid)
	}

	if gid := player.GetGroupId(); gid != "GID" {
		t.Errorf("wrong GID: %s instead of GID", gid)
	}

	if pid := player.GetId(); pid != "PID" {
		t.Errorf("wrong PID: %s instead of PID", pid)
	}

	if name := player.GetName(); name != "NAME" {
		t.Errorf("wrong name: %s instead of NAME", name)
	}

	if url := player.CreateFullRESTUrl("/blah"); url != "https://WSURL/api/v1/households/local/blah" {
		t.Errorf("wrong REST URL: %s instead of https://WSURL/api/v1/households/local/blah", url)
	}
}

//
// Websocket commnands, which are a wee bit more fun.  I'll start with a simple mock that just stashes the
// content sent to the callbacks.
//

func TestPlayerTargetedWebsocketCmd(t *testing.T) {
	cheese := newCheesyTestStuff(t)

	cheese.SendCommand("player", "getSettings")
	response := cheese.GetResponse()

	// Quick check of the response fields.  Should probably add checking this to cheese.
	if response.Headers.Success != true {
		t.Errorf("Command failed")
	}

	if response.Headers.Response != "Response, yo!" {
		t.Errorf("response mismatch")
	}

	// Make sure we did not get any events
	if cheese.GetEventCount() != 0 {
		t.Errorf("Received event instead of command completion?")
	}
}

func TestTimeout(t *testing.T) {
	cheese := newCheesyTestStuff(t)

	cheese.SetCommandTimeout(1*time.Millisecond, false)
	cheese.SendCommand("player", "getSettings")

	response := cheese.GetResponse()

	// Check some fields
	if response.Headers.Success == true {
		t.Errorf("Command worked?")
	}

	if response.Headers.Response != "Command timed out" {
		t.Errorf("Wrong response")
	}

	// Make sure we did not get any events
	if cheese.GetEventCount() != 0 {
		t.Errorf("Received event instead of command completion?")
	}
}

func TestCloseWithOutstandingCommands(t *testing.T) {
	cheese := newCheesyTestStuff(t)

	cheese.SetCommandTimeout(1*time.Second, false)

	cheese.SendCommand("player", "getSettings")
	cheese.CloseWebsocket()
	response := cheese.GetResponse()

	if response.Headers.Response != "The websocket has ceased to be.  It is a former websocket." {
		t.Errorf("wrong response")
	}
}

func TestEvents(t *testing.T) {
	cheese := newCheesyTestStuff(t)

	// FIXME:  Need to grab a real event to see what they look like again.  Just testing the plumbing anyway.
	event := sonos.WebsocketResponse{
		Headers: sonos.ResponseHeaders{
			CommonHeaders: sonos.CommonHeaders{
				Namespace:   "namespace",
				Command:     "command",
				UserId:      "",
				HouseholdId: "",
				GroupId:     "GID:PORT",
				PlayerId:    "PID",
				CmdId:       "",
				Topic:       "",
			},
			Response: "",
			Success:  true,
			Type:     "fakeEvent",
		},
		BodyJSON: []byte("{\"data\": \"blah\"}"),
	}

	cheese.InjectEvent(event)

	response := cheese.GetEvent()

	if response.Headers.Type != "fakeEvent" {
		t.Errorf("bogus response: %s != fakeEvent", response.Headers.Type)
	}

	type HackData struct {
		Data string `json:"data"`
	}

	data := HackData{}
	if err := json.Unmarshal(response.BodyJSON, &data); err != nil {
		t.Errorf("unable to get body back from event: %s", err.Error())
	}

	if data.Data != "blah" {
		t.Errorf("bogus data: %s != blah", data.Data)
	}
}
