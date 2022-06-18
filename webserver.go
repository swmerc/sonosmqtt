package main

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	log "github.com/sirupsen/logrus"
	"github.com/swmerc/sonosmqtt/sonos"

	"github.com/gorilla/mux"
)

type WebDataInterface interface {
	// Stuff we maintain internally.  Not that it matters.
	GetGroups() ([]byte, error)
	GetGroup(id string) ([]byte, error)
	GetPlayers() ([]byte, error)
	GetPlayer(id string) ([]byte, error)

	// Stuff that is just a passthrough to the normal Sonos API (currently via REST)
	GetDataREST(id string, namespace string, command string) ([]byte, error)
	PostDataREST(id string, namespace string, command string, body []byte) ([]byte, error)

	// Debug hackery to send a command over a websocket.
	CommandOverWebsocket(id string, namespace string, command string, callback func(sonos.WebsocketResponse)) error

	// Real function to send data over a websocket and await a response
	RequestOverWebsocket(request sonos.WebsocketRequest, callback func(sonos.WebsocketResponse))
}

type websocketUser struct {
	hash string
	ws   WebsocketClient
	mqtt mqtt.Client
	data WebDataInterface

	// Lock when accessing the above.  It is safe to take a reference of
	// ws and mqtt under the lock and use it later, but they may become nil
	// at any point so you do want to make sure it is still valid
	sync.Mutex
}

type websocketUsers struct {
	mutex sync.RWMutex
	users map[string]*websocketUser
}

var users = websocketUsers{
	mutex: sync.RWMutex{},
	users: make(map[string]*websocketUser),
}

func StartWebServer(port int, data WebDataInterface) {
	go func() {
		router := mux.NewRouter()

		// FIXME: Create a router for /api/v1/ to make the paths shorter?

		//
		// Simple GETs
		//
		router.HandleFunc("/api/v1/groups", func(w http.ResponseWriter, r *http.Request) {
			bytes, err := data.GetGroups()
			writeResponse(w, &bytes, err)
		}).Methods(http.MethodGet)

		router.HandleFunc("/api/v1/group/{id}", func(w http.ResponseWriter, r *http.Request) {
			bytes, err := data.GetGroup(mux.Vars(r)["id"])
			writeResponse(w, &bytes, err)
		}).Methods(http.MethodGet)

		router.HandleFunc("/api/v1/players", func(w http.ResponseWriter, r *http.Request) {
			bytes, err := data.GetPlayers()
			writeResponse(w, &bytes, err)
		}).Methods(http.MethodGet)

		router.HandleFunc("/api/v1/player/{id}", func(w http.ResponseWriter, r *http.Request) {
			bytes, err := data.GetPlayer(mux.Vars(r)["id"])
			writeResponse(w, &bytes, err)
		}).Methods(http.MethodGet)

		//
		// Commands that return unfiltered Sonos responses.  There is some magic mapping going on under
		// the covers, so you can pass the of any player in the group to get group information.
		//
		router.HandleFunc("/api/v1/player/{id}/{namespace}", func(w http.ResponseWriter, r *http.Request) {
			bytes, err := data.GetDataREST(mux.Vars(r)["id"], mux.Vars(r)["namespace"], "")
			writeResponse(w, &bytes, err)
		}).Methods(http.MethodGet)

		router.HandleFunc("/api/v1/player/{id}/{namespace}/{command}", func(w http.ResponseWriter, r *http.Request) {
			bytes, err := data.GetDataREST(mux.Vars(r)["id"], mux.Vars(r)["namespace"], mux.Vars(r)["command"])
			writeResponse(w, &bytes, err)
		}).Methods(http.MethodGet)

		router.HandleFunc("/api/v1/player/{id}/{namespace}/{command}", func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			bytes := make([]byte, 0)
			if err == nil {
				bytes, err = data.PostDataREST(mux.Vars(r)["id"], mux.Vars(r)["namespace"], mux.Vars(r)["command"], body)
			}
			writeResponse(w, &bytes, err)
		}).Methods(http.MethodPost)

		router.HandleFunc("/api/v1/wstest/{id}/{namespace}/{command}", func(w http.ResponseWriter, r *http.Request) {
			var responseChan chan sonos.WebsocketResponse
			err := data.CommandOverWebsocket(mux.Vars(r)["id"],
				mux.Vars(r)["namespace"],
				mux.Vars(r)["command"],
				func(resp sonos.WebsocketResponse) {
					responseChan <- resp
				})

			// If it failed immediately the callback was not set up.
			if err != nil {
				writeResponse(w, &[]byte{}, err)
				return
			}

			// If it did not fail immediately, it _will_ respond.
			response := <-responseChan
			raw, err := response.ToRawBytes()
			writeResponse(w, &raw, err)

		}).Methods(http.MethodPost)

		//
		// Websocket that can take Sonos control API commands and return events.  Wooo?
		//
		router.HandleFunc("/api/v1/ws", func(w http.ResponseWriter, r *http.Request) {
			handleWebsocketUpgrade(w, r, data)
		}).Methods(http.MethodGet)

		// Fire it up
		srv := &http.Server{
			Handler:      router,
			Addr:         fmt.Sprintf(":%d", port),
			WriteTimeout: 15 * time.Second,
			ReadTimeout:  15 * time.Second,
		}

		log.Fatal(srv.ListenAndServe())
	}()
}

func writeResponse(w http.ResponseWriter, data *[]byte, err error) {
	if err != nil {
		if err.Error() == "404" {
			w.WriteHeader(http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.Write(*data)
}

func handleWebsocketUpgrade(w http.ResponseWriter, r *http.Request, data WebDataInterface) {
	hash := r.RemoteAddr

	user := websocketUser{
		hash:  hash,
		ws:    nil,
		mqtt:  nil,
		data:  data,
		Mutex: sync.Mutex{},
	}

	ws := UpgradeToWebSocket(w, r, hash, &user)
	if ws == nil {
		http.Error(w, "unable to upgrade", http.StatusInternalServerError)
		return
	}

	user.ws = ws

	users.mutex.Lock()
	users.users[hash] = &user
	users.mutex.Unlock()
}

func (user *websocketUser) OnConnect(userdata string) {
	log.Infof("wsserver: connect: %s", userdata)

	client, err := initMQTTClient(false)
	if err != nil {
		log.Errorf("wsserver: can't connect to MQTT: %s", err.Error())
		return
	}

	user.Lock()
	user.mqtt = client
	user.Unlock()
}

func (user *websocketUser) OnClose(userdata string) {
	log.Infof("wsserver: close: %s", userdata)

	// Kill the MQTT client and make sure we remove references to the
	// MQTT client and websocket
	user.Lock()

	if user.mqtt != nil {
		user.mqtt.Disconnect(0)
	}
	user.mqtt = nil
	user.ws = nil
	user.data = nil

	user.Unlock()

	// Delete the user from the map, which should garbage collect it
	users.mutex.Lock()
	delete(users.users, user.hash)
	users.mutex.Unlock()
}

func (user *websocketUser) OnError(userdata string, err error) {
	log.Infof("wsserver: error: %s: %s", userdata, err.Error())
}

func (user *websocketUser) OnMessage(userdata string, bytes []byte) {
	log.Debugf("wsserver: message: %s: %s", userdata, string(bytes))

	// Parse the request
	request := sonos.WebsocketRequest{}
	if err := request.FromRawBytes(bytes); err != nil {
		// FIXME: Return a global error here?
		log.Errorf("wsserver: msg error: %s", err.Error())
		user.ws.SendMessage([]byte(err.Error()))
		return
	}

	// Grab a reference to the websocket under the lock
	user.Lock()
	wsClient := user.ws
	user.Unlock()

	// Pull out subscribes and use the MQTT client to subscribe.  This is the point
	// where I wish I had stashed the namespace in the MQTT topic, but screw it.  All
	// MQTT events can be a clean slate.
	//
	// FIXME: Move the MQTT handling to a new function, clean up error paths (emulating
	//        what players actually send), etc
	if request.Headers.Command == "subscribe" {
		log.Infof("subscribe: %s", request.Headers.Topic)

		success := true
		if user.mqtt == nil {
			success = false
		}

		log.Infof("wsserver: good topic, and haz client: %s", request.Headers.Topic)
		response := sonos.WebsocketResponse{
			Headers: sonos.ResponseHeaders{
				CommonHeaders: sonos.CommonHeaders{
					Command: "subscribe",
					CmdId:   request.Headers.CmdId,
					Topic:   request.Headers.Topic,
				},
				Success: success,
				Type:    "none",
			},
			BodyJSON: []byte{},
		}

		body, err := response.ToRawBytes()
		if err != nil {
			log.Errorf("wsserver: can't convert response to JSON: %s", err.Error())
		} else {
			user.ws.SendMessage(body)
		}

		if success {
			user.mqtt.Subscribe(request.Headers.Topic, 0, func(client mqtt.Client, msg mqtt.Message) {
				if wsClient != nil {
					event := sonos.WebsocketResponse{
						Headers: sonos.ResponseHeaders{
							CommonHeaders: sonos.CommonHeaders{
								Topic: msg.Topic(),
							},
						},
						BodyJSON: []byte(msg.Payload()),
					}

					body, err := event.ToRawBytes()
					if err != nil {
						log.Errorf("wsserver: can't convert event to JSON: %s", err.Error())
					} else {
						wsClient.SendMessage(body)
					}
				}
			})
		}
		return
	}

	// Send it along and reply when we get a response from the player
	log.Infof("OnMessage: sending: %v", request)
	user.data.RequestOverWebsocket(request, func(response sonos.WebsocketResponse) {
		response.Headers.CmdId = request.Headers.CmdId
		log.Infof("OnMessage: response: %v", response)
		raw, err := response.ToRawBytes()
		if err != nil {
			log.Errorf("OnMessage: conversion failed: %s", err)
		} else {
			wsClient.SendMessage(raw)
		}
	})
}
