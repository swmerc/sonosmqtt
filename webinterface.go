package main

import (
	"encoding/json"
	"fmt"
)

//
// Interface to the webserver that is part of App.  None of this is running on our main goroutine, so we
// grab a copy of the data we need under a lock.
//

// Simplified version of groups.  The internal version is a map of groups containing maps of players, this
// is just a coordinatorId and a slice of players in the group.
type ExportedGroup struct {
	CoordinatorId string    `json:"id"`
	Players       []*Player `json:"players"`
}

func exportedGroupFromGroup(group Group) ExportedGroup {
	exported := ExportedGroup{
		CoordinatorId: group.Coordinator.PlayerId,
		Players:       make([]*Player, 0, 64),
	}

	for _, player := range group.Players {
		exported.Players = append(exported.Players, player)
	}

	return exported
}

// GetGroups returns a list of al ExportedGroups
func (a *App) GetGroups() ([]byte, error) {
	groups := make([]ExportedGroup, 0, 64)

	a.groupsLock.RLock()
	for _, group := range a.groups {
		groups = append(groups, exportedGroupFromGroup(group))
	}
	a.groupsLock.RUnlock()

	return json.Marshal(groups)
}

// GetGroup returns a single ExportedGroup with the matching CoordinatorId
func (a *App) GetGroup(id string) ([]byte, error) {

	a.groupsLock.RLock()
	group, ok := a.groups[id]
	a.groupsLock.RUnlock()

	if ok {
		return json.Marshal(exportedGroupFromGroup(group))
	}

	return nil, fmt.Errorf("404")
}

func (a *App) GetPlayers() ([]byte, error) {
	players := make([]*Player, 0, 64)

	a.groupsLock.RLock()
	for _, group := range a.groups {
		for _, player := range group.Players {
			players = append(players, player)
		}
	}
	a.groupsLock.RUnlock()

	return json.Marshal(players)
}

func (a *App) GetPlayer(id string) ([]byte, error) {
	player := &Player{}
	ok := false

	a.groupsLock.RLock()
	for _, group := range a.groups {
		player, ok = group.Players[id]
		if ok {
			break
		}
	}
	a.groupsLock.RUnlock()

	if ok {
		return json.Marshal(player)
	}

	return nil, fmt.Errorf("404")
}

var playerTargetedCommands = map[string]bool{
	"settings":     true,
	"playerVolume": true,
}

func getPlayerForNamespace(groupMap *map[string]Group, id string, namespace string) (Player, string) {

	playerTargeted := playerTargetedCommands[namespace]

	player := Player{}
	for _, g := range *groupMap {
		if p, ok := g.Players[id]; ok {
			if playerTargeted {
				player = *p
			} else {
				player = *g.Coordinator
			}
			break
		}
	}

	path := ""
	if playerTargeted {
		path = fmt.Sprintf("/players/%s", player.PlayerId)
	} else {
		path = fmt.Sprintf("/groups/%s", player.GroupId)
	}

	return player, path
}

func (a *App) GetDataREST(id string, namespace string, object string) ([]byte, error) {
	a.groupsLock.RLock()
	player, path := getPlayerForNamespace(&a.groups, id, namespace)
	a.groupsLock.RUnlock()

	if player.PlayerId == "" {
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
	return a.playerDoGET(&player, fullpath)
}

func (a *App) PostDataREST(id string, namespace string, command string, body []byte) ([]byte, error) {
	a.groupsLock.RLock()
	player, path := getPlayerForNamespace(&a.groups, id, namespace)
	a.groupsLock.RUnlock()

	if player.PlayerId == "" {
		return nil, fmt.Errorf("404")
	}

	return a.playerDoPOST(&player, fmt.Sprintf("%s/%s/%s", path, namespace, command), body)
}
