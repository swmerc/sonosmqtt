package main

import (
	"encoding/json"
	"fmt"
)

// FIXME: I eventually want to kick this into another package.  The naming issues are a pain (MusePlayer vs Player, for example).

// Returned from /api/v1/player/local/info
type MusePlayerInfoResponse struct {
	Device struct {
		Name string `json:"name"`
	} `json:"device"`
	HouseholdId  string `json:"householdId"`
	GroupId      string `json:"groupId"`
	PlayerId     string `json:"playerId"`
	WebsocketUrl string `json:"websocketUrl"`
	RestUrl      string `json:"restUrl"`
}

// Returned from /api/v1/households/local/groups
type MuseGroup struct {
	Id              string   `json:"id"`
	Name            string   `json:"name"`
	CoordinatorId   string   `json:"coordinatorId"`
	PlayerbackState string   `json:"playbackState"`
	PlayerIds       []string `json:"playerIds"`
}

type MusePlayer struct {
	Id           string   `json:"id"`
	Name         string   `json:"name"`
	WebsocketUrl string   `json:"websocketUrl"`
	Capabilities []string `json:"capabilities"`
}

type MuseGroupsResponse struct {
	Groups  []MuseGroup  `json:"groups"`
	Players []MusePlayer `json:"players"`
}

type MusePlaybackState struct {
	PlaybackState string `json:"playbackState"`
}

// All possible headers.  Probably should split into request and response, but whatever.
type MuseHeaders struct {
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

// The response is parsed headers and unparsed body.  This allows cleaner parsing in the
// code that deals with the body.
type MuseResponse struct {
	Headers  MuseHeaders
	BodyJSON []byte
}

func (museResponse *MuseResponse) fromRawBytes(data []byte) error {
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
	if err = json.Unmarshal(headerJSON, &museResponse.Headers); err != nil {
		return err
	}

	// See if we got success
	//
	// This only matters for commands, not events we receive due to subscriptions.  This should
	// be cleaned up a bit to track subscriptions and missing command responses, but it kind
	// of works for now.
	if museResponse.Headers.CmdId != "" && museResponse.Headers.Success != true {
		return fmt.Errorf("MuseResponse failed: %s: %v", museResponse.Headers.Namespace, parsed[0])
	}

	// Recreate the body JSON so we can parse it again later.  WOOO?
	if museResponse.BodyJSON, err = json.Marshal(parsed[1]); err != nil {
		return err
	}

	return nil
}
