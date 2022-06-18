package main

import (
	"log"
	"net/http"
	"testing"
	"time"

	sonos "github.com/swmerc/sonosmqtt/sonos"
)

//
// Object creation
//
func CreatePlayerFromInfoResponse() Player {
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

func TestNewInternalPlayerFromInfoResponse(t *testing.T) {
	player := CreatePlayerFromInfoResponse()

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

// MockWebsocketClient implements the WebsocketClient interface
type MockWebsocketClient struct {
	userData  string
	message   []byte
	callbacks WebsocketCallbacks
	closed    bool
}

var respondToMessages bool = true

func (ws *MockWebsocketClient) SendMessage(data []byte) error {
	request := sonos.WebsocketRequest{}
	if err := request.FromRawBytes(data); err != nil {
		log.Fatalf("can't parse request")
	}

	if respondToMessages {
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
}

func (ws *MockWebsocketClient) IsRunning() bool {
	return !ws.closed
}

func NewMockedClientSideWebsocket(url string, userData string, headers http.Header, callbacks WebsocketCallbacks) WebsocketClient {
	client := &MockWebsocketClient{
		userData:  userData,
		message:   []byte{},
		callbacks: callbacks,
		closed:    false,
	}

	return client
}

type MockEventHandler struct {
	playerId string
	response sonos.WebsocketResponse
	err      error
}

func (m *MockEventHandler) OnEvent(playerId string, response sonos.WebsocketResponse) {
	m.playerId = playerId
	m.response = response
}

func (m *MockEventHandler) OnError(playerId string, err error) {
	m.playerId = playerId
	m.err = err
}

func TestPlayerTargetedWebsocketCmd(t *testing.T) {
	eventHandler := MockEventHandler{}

	websocketInitHook = NewMockedClientSideWebsocket

	var responseChan chan sonos.WebsocketResponse = make(chan sonos.WebsocketResponse, 5)
	player := CreatePlayerFromInfoResponse()
	player.InitWebsocketConnection(http.Header{}, &eventHandler)

	err := player.SendCommandViaWebsocket("player", "getSettings", func(response sonos.WebsocketResponse) {
		responseChan <- response
	})

	if err != nil {
		t.Errorf("send failed: %s", err.Error())
	}

	response := <-responseChan
	if response.Headers.Success != true {
		t.Errorf("Command failed")
	}

	if response.Headers.Response != "Response, yo!" {
		t.Errorf("response mismatch")
	}
}

func TestTimeout(t *testing.T) {
	respondToMessages = false
	playerCmdTimeout = 25 * time.Millisecond

	defer func() {
		respondToMessages = true
	}()

	eventHandler := MockEventHandler{}
	websocketInitHook = NewMockedClientSideWebsocket

	var responseChan chan sonos.WebsocketResponse = make(chan sonos.WebsocketResponse, 5)
	player := CreatePlayerFromInfoResponse()
	player.InitWebsocketConnection(http.Header{}, &eventHandler)

	err := player.SendCommandViaWebsocket("player", "getSettings", func(response sonos.WebsocketResponse) {
		responseChan <- response
	})

	if err != nil {
		t.Errorf("send failed: %s", err.Error())
	}

	response := <-responseChan

	if response.Headers.Success == true {
		t.Errorf("Command worked?")
	}

	if response.Headers.Response != "Command timed out" {
		t.Errorf("Wrong response")
	}

}
