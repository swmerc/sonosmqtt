package main

import (
	"bytes"
	"encoding/json"
)

// simplifyMuseType converts between the possibly complex type returned by Muse to a much
// simpler type suitable for a dumb device.
//
// FIXME: I should probably pass msg to the simplifiers so they can change the Type as
//        well.  Too hacky to assume it here.
func simplifyMuseType(msg *MuseResponseWithId) {
	if f, ok := simplfiers[msg.Headers.Type]; ok {
		if body, err := f(msg.MuseResponse.BodyJSON); err == nil {
			msg.Headers.Type = msg.Headers.Type + "Simple"
			msg.BodyJSON = body

			// Yup, this has got to go
			if msg.Headers.Type == "groupsSimple" {
				msg.Headers.Type = "playersSimple"
			}
		}
	}
}

var simplfiers = map[string]func([]byte) ([]byte, error){
	"extendedPlaybackStatus": simplifyPlaybackExtended,
	"groups":                 simplifyGroups,
}

//
// Translate MuseExtendedPlaybackStatus to our simpler format. Note that this is FAR from
// the complete message, but it doesn't matter.  We just need to define the stuff we intend
// to read from it.
//
type MuseExtendedPlaybackStatus struct {
	PlaybackState MusePlaybackState `json:"playback"`
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

type SimpleExtendedPlaybackStatus struct {
	PlaybackState string `json:"playbackState"`
	Artist        string `json:"artist,omitempty"`
	Album         string `json:"album,omitempty"`
	Track         string `json:"track,omitempty"`
	Service       string `json:"service,omitempty"`
	ImageUrl      string `json:"imageUrl,omitempty"`
}

func simplifyPlaybackExtended(body []byte) ([]byte, error) {

	sonosMsg := MuseExtendedPlaybackStatus{}
	if err := json.Unmarshal(body, &sonosMsg); err != nil {
		return nil, err
	}

	// Treat buffering like playing for now to cut down on events
	playbackState := sonosMsg.PlaybackState.PlaybackState
	if playbackState == "PLAYBACK_STATE_BUFFERING" {
		playbackState = "PLAYBACK_STATE_PLAYING"
	}

	// Convert
	track := &sonosMsg.Metadata.CurrentItem.Track
	simpleMsg := SimpleExtendedPlaybackStatus{
		PlaybackState: playbackState,
		Artist:        track.Artist.Name,
		Album:         track.Album.Name,
		Track:         track.Name,
		Service:       track.Service.Name,
		ImageUrl:      track.ImageUrl,
	}

	return marshalWithNoHtmlEscape(simpleMsg)
}

//
// groups, which turn into players when simplified
//
type SimplePlayer struct {
	Id   string `json:"id"`
	Name string `json:"name"`
}

func simplifyGroups(body []byte) ([]byte, error) {

	// Validate
	sonosMsg := MuseGroupsResponse{}
	if err := json.Unmarshal(body, &sonosMsg); err != nil {
		return nil, err
	}

	// Convert
	players := make([]SimplePlayer, 0, 32)
	for _, p := range sonosMsg.Players {
		players = append(players, SimplePlayer{Id: p.Id, Name: p.Name})
	}

	return marshalWithNoHtmlEscape(players)
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
