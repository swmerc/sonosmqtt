package main

import sonos "github.com/swmerc/sonosmqtt/sonos"

// Group contains the information required to turn group events into individual player events.
//
// The Coordinator is the player that is actually downloading/playing the content.  It sends
// this data to the other players.  Note that I left the cooridinator in the map of players
// to make it easier to iterate over at the expense of a little data.
//
// Note that we have a TON of copies of the PlayerId for now (and perhaps always).  Oh well.
type Group struct {
	Coordinator *Player
	Players     map[string]*Player
}

// getGroupMap parses a sonos.GroupsResponse and returns a map of all Groups indexed by PlayerId.
//
// This is used internally to track the group/player relationships instead of using the Sonos
// data structtures.  I suppose I should make a proper type for the map[string]Group stuff at
// some point.
func getGroupMap(hhid string, groupsResponse sonos.GroupsResponse) (map[string]Group, error) {
	var allPlayers map[string]*Player = make(map[string]*Player, 32)
	var allGroups map[string]Group = make(map[string]Group, 32)

	// Stash all of the players
	for _, p := range groupsResponse.Players {
		player := newInternalPlayerFromSonosPlayer(p, hhid, "") // We don't know GroupId yet
		allPlayers[player.PlayerId] = player
	}

	// Process the groups and create them from the players
	for _, group := range groupsResponse.Groups {
		if coordinator, ok := allPlayers[group.CoordinatorId]; ok {

			// We now know groupId.  This is good because we need it for the command headers later.
			//
			// I could likely pull this out of Player and toss it into Group when we subscribe to all groups?
			coordinator.GroupId = group.Id

			players := make(map[string]*Player, 32)
			for _, playerId := range group.PlayerIds {
				if player, ok := allPlayers[playerId]; ok {
					player.GroupId = group.Id
					player.CoordinatorId = coordinator.PlayerId
					players[player.PlayerId] = player
				}
			}

			newGroup := Group{
				Coordinator: coordinator,
				Players:     players,
			}
			allGroups[coordinator.PlayerId] = newGroup
		}
	}

	return allGroups, nil
}

// groupsAreCloseEnoughForMe() returns true if two group maps match.
func groupsAreCloseEnoughForMe(a, b map[string]Group) bool {

	// Quick and dirty length check.
	if len(a) != len(b) {
		return false
	}

	// Number of groups matches, make sure each one matches
	for id, group := range a {

		// Miss
		groupMatch, ok := b[id]
		if !ok {
			return false
		}

		// Hit with different number of players
		if len(group.Players) != len(groupMatch.Players) {
			return false
		}

		// Walk the players.  Almost done.
		for id := range group.Players {
			if _, ok := groupMatch.Players[id]; !ok {
				return false
			}
		}

	}

	return true
}
