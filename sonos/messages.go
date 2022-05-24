package sonos

import (
	"encoding/json"
	"fmt"
)

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

// Overall response format, which is parsed headers and an unparsed body.  This allows cleaner
// parsing in the code that deals with the body since we can do it later when we know the type.
type Response struct {
	Headers  Headers
	BodyJSON []byte
}

// All possible headers.  Probably should split into request and response, but whatever.
type Headers struct {
	// Could be in either request or response
	Namespace   string `json:"namespace"`
	Command     string `json:"command"`
	GroupId     string `json:"groupId,omitempty"`
	PlayerId    string `json:"playerId,omitempty"`
	HouseholdId string `json:"householdId,omitempty"`
	CmdId       string `json:"cmdId,omitempty"`

	// Only response
	Response string `json:"response,omitempty"`
	Success  bool   `json:"success,omitempty"`
	Type     string `json:"type,omitempty"`
}

func (response *Response) FromRawBytes(data []byte) error {
	// Muse responses are a two entry array.  The first entry is MuseHeaders, and the
	// second is the body, which is the actual data we want.  I kid you not.
	//
	// This bit parses the array into the two elements and confirms that there are
	// exactly two.
	var parsed []interface{}
	err := json.Unmarshal(data, &parsed)
	if err == nil && len(parsed) != 2 {
		err = fmt.Errorf("Unexpected array length")
	}
	if err != nil {
		return err
	}

	// We now recreate the original JSON so we can parse it into the proper structure.
	headerJSON, err := json.Marshal(parsed[0])
	if err != nil {
		return err
	}

	// Parse the JSON a second time to put it in the header
	if err = json.Unmarshal(headerJSON, &response.Headers); err != nil {
		return err
	}

	// See if we got success
	//
	// This only matters for commands, not events we receive due to subscriptions.  This should
	// be cleaned up a bit to track subscriptions and missing command responses, but it kind
	// of works for now.
	if response.Headers.CmdId != "" && response.Headers.Success != true {
		return fmt.Errorf("MuseResponse failed: %s: %v", response.Headers.Namespace, parsed[0])
	}

	// Recreate the body JSON so we can parse it again later.  WOOO?
	if response.BodyJSON, err = json.Marshal(parsed[1]); err != nil {
		return err
	}

	return nil
}
