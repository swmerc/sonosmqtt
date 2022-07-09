package sonos

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ConvertToApiVersion1 replaces v2 in api versions with v1.  At least for now.  I'll eventually have to support v2 when/if
// Sonos ever ships any changes.
func ConvertToApiVersion1(url string) string {
	return strings.Replace(url, "/v2/", "/v1/", 1)
}

//
// Support for determing whether a command is for a player instead of a group.  There has to be an easier way to
// deal with all of this, but for now I don't care about household stuff so anything that is not player is group.
// Probably.
//

var playerTargetedCommands = map[string]bool{
	"settings":     true,
	"playerVolume": true,
}

func IsPlayerTargetedCommand(namespace string) bool {
	_, ok := playerTargetedCommands[namespace]
	return ok
}

//
// Specific responses we care about
//

// Returned from /api/v1/player/local/info
type PlayerInfoResponse struct {
	Device struct {
		Name string `json:"name"`
	} `json:"device"`
	HouseholdId  string `json:"householdId"`
	GroupId      string `json:"groupId"`
	PlayerId     string `json:"playerId"`
	WebsocketUrl string `json:"websocketUrl"`
	RestUrl      string `json:"restUrl"`
}

// Returned from /api/v1/households/local/groups, and evented when subscribing to groups
type GroupsResponse struct {
	Groups  []Group  `json:"groups"`
	Players []Player `json:"players"`
}

type Group struct {
	Id              string   `json:"id"`
	Name            string   `json:"name"`
	CoordinatorId   string   `json:"coordinatorId"`
	PlayerbackState string   `json:"playbackState"`
	PlayerIds       []string `json:"playerIds"`
}

type Player struct {
	Id           string   `json:"id"`
	Name         string   `json:"name"`
	WebsocketUrl string   `json:"websocketUrl"`
	Capabilities []string `json:"capabilities"`
}

type PlaybackState struct {
	PlaybackState string `json:"playbackState"`
}

// ExtendedPlaybackStatus, which is evented when subscribing to playbackExtended.  This is
// *not* the complete content, only the stuff that I care about for the moment.
type ExtendedPlaybackStatus struct {
	PlaybackState PlaybackState `json:"playback"`
	Metadata      struct {
		CurrentItem struct {
			Track struct {
				Type     string `json:"type"`
				Name     string `json:"name"`
				ImageUrl string `json:"imageUrl"`
				Album    struct {
					Name string `json:"name"`
				} `json:"album"`
				Artist struct {
					Name string `json:"name"`
				} `json:"artist"`
				Service struct {
					Name string `json:"name"`
				} `json:"service"`
			} `json:"track"`
		} `json:"currentItem"`
	} `json:"Metadata"`
}

// CommonHeaders are headers that are common to requests and responses.  This saves
// me some typing.
type CommonHeaders struct {
	// Resource being accessed
	Namespace string `json:"namespace"`
	Command   string `json:"command"`

	// Ids, which I suppose help define the resource
	UserId      string `json:"userId,omitempty"`
	HouseholdId string `json:"householdId,omitempty"`
	GroupId     string `json:"groupId,omitempty"`
	PlayerId    string `json:"playerId,omitempty"`

	// CmdId, which helps associate the response with the
	// request on the caller. Think of it as userData in a
	// callback.
	CmdId string `json:"cmdId,omitempty"`

	// MQTT topic for subscriptions.  Only in my hacky version.
	Topic string `json:"topic,omitempty"`
}

//
// Requests over websockets
//

// RequestHeaders are the headers present for requests
type RequestHeaders struct {
	CommonHeaders
}

// WebSocketRequest is simply headers and a body
type WebsocketRequest struct {
	Headers  RequestHeaders
	BodyJSON []byte
}

func (request *WebsocketRequest) ToRawBytes() ([]byte, error) {
	return webSocketMessageToRawBytes(request.Headers, request.BodyJSON)
}

func (msg *WebsocketRequest) FromRawBytes(data []byte) error {
	return webSocketMessageFromRawBytes(data, &msg.Headers, &msg.BodyJSON)
}

//
// Websocket responses
//

type ResponseHeaders struct {
	CommonHeaders
	Response string `json:"response,omitempty"`
	Success  bool   `json:"success,omitempty"`
	Type     string `json:"type,omitempty"`
}

type WebsocketResponse struct {
	Headers  ResponseHeaders
	BodyJSON []byte
}

func (msg *WebsocketResponse) ToRawBytes() ([]byte, error) {
	return webSocketMessageToRawBytes(msg.Headers, msg.BodyJSON)
}

func (msg *WebsocketResponse) FromRawBytes(data []byte) error {
	if err := webSocketMessageFromRawBytes(data, &msg.Headers, &msg.BodyJSON); err != nil {
		return err
	}

	// Check for success on commands
	// if msg.Headers.CmdId != "" && !msg.Headers.Success {
	// 	return fmt.Errorf("response is for a failed command: cmdId=%s", msg.Headers.CmdId)
	// }

	return nil
}

//
// Helper functions for marshaling and unmarshaling messages received over websockets
//

func webSocketMessageToRawBytes(headers interface{}, body []byte) ([]byte, error) {
	headersJSON, err := json.Marshal(headers)
	if err != nil {
		return nil, err
	}

	if body == nil || len(body) < 1 {
		body = []byte("{}")
	}

	return []byte(fmt.Sprintf("[%s,%s]", headersJSON, string(body))), nil
}

func webSocketMessageFromRawBytes(dataIn []byte, headersOut interface{}, dataOut *[]byte) error {
	var parsedArray []interface{}
	err := json.Unmarshal(dataIn, &parsedArray)
	if err == nil && len(parsedArray) != 2 {
		err = fmt.Errorf("unexpected array length: %d", len(parsedArray))
	}
	if err != nil {
		return err
	}

	// We now recreate the original JSON so we can parse it into the proper structure.
	headerJSON, err := json.Marshal(parsedArray[0])
	if err != nil {
		return err
	}

	// Parse the JSON a second time to put it in the header
	if err = json.Unmarshal(headerJSON, headersOut); err != nil {
		return err
	}

	// Recreate the body JSON so we can parse it again later.  WOOO?
	if *dataOut, err = json.Marshal(parsedArray[1]); err != nil {
		return err
	}

	return nil
}
