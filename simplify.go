package main

import (
	"bytes"
	"encoding/json"
	"net/url"

	sonos "github.com/swmerc/sonosmqtt/sonos"
)

// simplifySonosType converts between the possibly complex type returned by Sonos to a much
// simpler type suitable for a dumb device.
func simplifySonosType(msg *SonosResponseWithId) bool {
	if f, ok := simplfiers[msg.Headers.Type]; ok {
		if body, err := f(msg.WebsocketResponse.BodyJSON); err == nil {
			msg.Headers.Type = msg.Headers.Type + "Simple"
			msg.BodyJSON = body
			return true
		}
	}
	return false
}

var simplfiers = map[string]func([]byte) ([]byte, error){
	"extendedPlaybackStatus": simplifyPlaybackExtended,
	"groups":                 simplifyGroups,
}

type SimpleExtendedPlaybackStatus struct {
	PlaybackState string `json:"playbackState"`
	Artist        string `json:"artist,omitempty"`
	Album         string `json:"album,omitempty"`
	Track         string `json:"track,omitempty"`
	Service       string `json:"service,omitempty"`
	ImageUrl      string `json:"imageUrl,omitempty"`
}

func simplifyPlaybackExtended(body []byte) ([]byte, error) {

	sonosMsg := sonos.ExtendedPlaybackStatus{}
	if err := json.Unmarshal(body, &sonosMsg); err != nil {
		return nil, err
	}

	// Treat buffering like playing for now to cut down on events
	playbackState := sonosMsg.PlaybackState.PlaybackState
	if playbackState == "PLAYBACK_STATE_BUFFERING" {
		playbackState = "PLAYBACK_STATE_PLAYING"
	}

	// Convert, double decoding imageUrl to work around a Sonos encoding bug
	track := &sonosMsg.Metadata.CurrentItem.Track
	imageUrl, _ := url.QueryUnescape(track.ImageUrl)
	imageUrl, _ = url.QueryUnescape(imageUrl)

	simpleMsg := SimpleExtendedPlaybackStatus{
		PlaybackState: playbackState,
		Artist:        track.Artist.Name,
		Album:         track.Album.Name,
		Track:         track.Name,
		Service:       track.Service.Name,
		ImageUrl:      imageUrl,
	}

	return marshalWithNoHtmlEscape(simpleMsg)
}

type SimplePlayer struct {
	Id   string `json:"id"`
	Name string `json:"name"`
}

type SimpleGroup struct {
	Id      string         `json:"id"`
	Players []SimplePlayer `json:"players"`
}

func simplifyGroups(body []byte) ([]byte, error) {

	// Parse the message
	sonosMsg := sonos.GroupsResponse{}
	if err := json.Unmarshal(body, &sonosMsg); err != nil {
		return nil, err
	}

	// Stash all of the players
	var allPlayers map[string]SimplePlayer = make(map[string]SimplePlayer, 64)
	for _, p := range sonosMsg.Players {
		player := SimplePlayer{
			Id:   p.Id,
			Name: p.Name,
		}
		allPlayers[player.Id] = player
	}

	// Walk the groups and append to allGroups
	allGroups := make([]SimpleGroup, 0, 64)
	for _, g := range sonosMsg.Groups {

		group := SimpleGroup{
			Id:      g.CoordinatorId,
			Players: make([]SimplePlayer, 0, 64),
		}

		if groupCoordinator, ok := allPlayers[g.CoordinatorId]; ok {
			group.Players = append(group.Players, groupCoordinator)
		}

		for _, p := range g.PlayerIds {
			if player, ok := allPlayers[p]; ok {
				group.Players = append(group.Players, player)
			}
		}

		allGroups = append(allGroups, group)
	}

	return json.Marshal(allGroups)
}

//
// Helper for marshalling without HTML escaping
//
func marshalWithNoHtmlEscape(v interface{}) ([]byte, error) {
	buffer := bytes.NewBuffer([]byte{})

	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(v); err != nil {
		return nil, err
	}

	return buffer.Bytes(), nil
}
