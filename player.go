package main

import (
	"fmt"
	"strings"

	sonos "github.com/swmerc/sonosmqtt/sonos"
)

type Player struct {
	// Stuff from /info or /groups.  Not quite static, but we'll regenerate if GroupId changes so this is
	// all static enough for our purposes.
	Name          string
	PlayerId      string
	GroupId       string
	CoordinatorId string
	HouseholdId   string
	RestUrl       string
	WebsocketUrl  string

	CmdId int32

	// Websocket to the player.  All players we are tracking will have one.
	Websocket WebsocketClient
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
// Functions to generate all of the data we need to talk to a player from a couple of sources.  I suppose
// I could just use one of the existing structs from Sonos responses and add in what I need.
//

// newInternalPlayerFromInfoResponse takes the data returned from /info and turns it into
// our internal format.  Stuff not included is generated.
func newInternalPlayerFromInfoResponse(info sonos.PlayerInfoResponse) *Player {
	return &Player{
		Name:          info.Device.Name,
		PlayerId:      info.PlayerId,
		GroupId:       info.GroupId,
		CoordinatorId: groupIdToCoordinatorId(info.GroupId),
		HouseholdId:   info.HouseholdId,
		RestUrl:       info.RestUrl,
		WebsocketUrl:  info.WebsocketUrl,
		CmdId:         1,
		Websocket:     nil,
	}
}

// newInternalPlayerFromSonos takes the player data returned in things like GroupsResponse and
// turns it into our internal format.  No, this is not the same data we get from /info, or at
// least it was not at some point.  The RestUrl may be included now.
func newInternalPlayerFromSonosPlayer(player sonos.Player, householdId string, groupId string) *Player {
	return &Player{
		Name:          player.Name,
		PlayerId:      player.Id,
		GroupId:       groupId,
		CoordinatorId: groupIdToCoordinatorId(groupId),
		HouseholdId:   householdId,
		RestUrl:       restUrlFromWebsocketUrl(player.WebsocketUrl),
		WebsocketUrl:  player.WebsocketUrl,
		CmdId:         1,
		Websocket:     nil,
	}
}

func (p *Player) String() string {
	return fmt.Sprintf("name=%s, id=%s, groupid=%s, wsurl=%s, resturl=%s", p.Name, p.PlayerId, p.GroupId, p.WebsocketUrl, p.RestUrl)
}
