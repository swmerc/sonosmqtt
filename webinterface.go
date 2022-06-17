package main

import (
	"encoding/json"
	"fmt"

	"github.com/swmerc/sonosmqtt/sonos"
)

//
// Interface to the webserver that is part of App.  None of this is running on our main goroutine, so we
// grab a copy of the data we need under a lock.
//

// Simplified version of groups.  The internal version is a map of groups containing maps of players, this
// is just a coordinatorId and a slice of players in the group.
type ExportedGroup struct {
	CoordinatorId string   `json:"id"`
	Players       []Player `json:"players"`
}

func exportedGroupFromGroup(group Group) ExportedGroup {
	exported := ExportedGroup{
		CoordinatorId: group.Coordinator.GetId(),
		Players:       make([]Player, 0, 64),
	}

	for _, player := range group.Players {
		exported.Players = append(exported.Players, player)
	}

	return exported
}

// GetGroups returns a list of al ExportedGroups
func (app *App) GetGroups() ([]byte, error) {
	groups := make([]ExportedGroup, 0, 64)

	app.groupsLock.RLock()
	for _, group := range app.groups {
		groups = append(groups, exportedGroupFromGroup(group))
	}
	app.groupsLock.RUnlock()

	return json.Marshal(groups)
}

// GetGroup returns a single ExportedGroup with the matching CoordinatorId
func (app *App) GetGroup(id string) ([]byte, error) {

	app.groupsLock.RLock()
	group, ok := app.groups[id]
	app.groupsLock.RUnlock()

	if ok {
		return json.Marshal(exportedGroupFromGroup(group))
	}

	return nil, fmt.Errorf("404")
}

func (app *App) GetPlayers() ([]byte, error) {
	players := make([]Player, 0, 64)

	app.groupsLock.RLock()
	for _, group := range app.groups {
		for _, player := range group.Players {
			players = append(players, player)
		}
	}
	app.groupsLock.RUnlock()

	return json.Marshal(players)
}

func (app *App) GetPlayer(id string) ([]byte, error) {
	var player Player = nil
	ok := false

	app.groupsLock.RLock()
	for _, group := range app.groups {
		player, ok = group.Players[id]
		if ok && player != nil {
			break
		}
		ok = false
	}
	app.groupsLock.RUnlock()

	if ok {
		return json.Marshal(player)
	}

	return nil, fmt.Errorf("404")
}

func getPlayerForNamespace(groupMap *map[string]Group, id string, namespace string) (Player, string) {

	playerTargeted := sonos.IsPlayerTargetedCommand(namespace)

	var player Player = nil

	for _, g := range *groupMap {
		if p, ok := g.Players[id]; ok {
			if playerTargeted {
				player = p
			} else {
				player = g.Coordinator
			}
			break
		}
	}

	if player == nil {
		return nil, ""
	}

	path := ""
	if playerTargeted {
		path = fmt.Sprintf("/players/%s", player.GetId())
	} else {
		path = fmt.Sprintf("/groups/%s", player.GetGroupId())
	}

	return player, path
}

func (app *App) GetDataREST(id string, namespace string, object string) ([]byte, error) {
	app.groupsLock.RLock()
	player, path := getPlayerForNamespace(&app.groups, id, namespace)
	app.groupsLock.RUnlock()

	if player == nil {
		return nil, fmt.Errorf("404")
	}

	// Just proxy it and hope for the best.  Royal pain that amespaces that contain a single
	// variable only need the namespace.  The API would be better if you always supplied the
	// namespace and object, but what do I know?
	fullpath := ""
	if len(object) > 0 {
		fullpath = fmt.Sprintf("%s/%s/%s", path, namespace, object)
	} else {
		fullpath = fmt.Sprintf("%s/%s", path, namespace)
	}
	return app.playerDoGET(player, fullpath)
}

func (app *App) PostDataREST(id string, namespace string, command string, body []byte) ([]byte, error) {
	app.groupsLock.RLock()
	player, path := getPlayerForNamespace(&app.groups, id, namespace)
	app.groupsLock.RUnlock()

	if player == nil {
		return nil, fmt.Errorf("404")
	}

	return app.playerDoPOST(player, fmt.Sprintf("%s/%s/%s", path, namespace, command), body)
}

func (app *App) CommandOverWebsocket(id string, namespace string, command string) ([]byte, error) {
	app.groupsLock.RLock()
	player, _ := getPlayerForNamespace(&app.groups, id, namespace)
	app.groupsLock.RUnlock()

	if player == nil {
		return nil, fmt.Errorf("404")
	}

	// Form a message and fire it down the websocket
	if err := player.SendCommandViaWebsocket(namespace, command); err != nil {
		return nil, fmt.Errorf("500: %s", err.Error())
	}

	// Assume success, at least for now.   This is just a test to make sure I understand how websocket
	// commands can be mapped to REST.
	return []byte(""), nil
}

func (app *App) WebsocketToREST(msg sonos.WebsocketRequest) []byte {
	headers := &msg.Headers

	bytesFromPlayer, err := app.PostDataREST(headers.PlayerId, headers.Namespace, headers.Command, msg.BodyJSON)

	// FIXME: We want to return the same headers the Sonos player does for a websocket request.  Guess
	//        I need to figure out what those are.
	response := sonos.WebsocketResponse{
		Headers: sonos.ResponseHeaders{
			CommonHeaders: msg.Headers.CommonHeaders,
		},
		BodyJSON: bytesFromPlayer,
	}

	if err == nil {
		response.Headers.Success = true
		response.Headers.Type = "ummmmm"
	} else {
		response.Headers.Success = true
		response.Headers.Type = "globalError"
	}

	body, _ := response.ToRawBytes()
	return body
}

// CODEME: I need to be able to pass websocket messages through to the player's websocket so I don't need to
//         do any translation.  The only hard part is stashing a callback to deal with the response, which
//         in turn requires a timeout mechanism and a way to respond to all waiters with "nope, the websocket bounced"
//         if things bounce.  Sounds like fun.
