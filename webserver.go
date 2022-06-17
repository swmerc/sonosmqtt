package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
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

	// Function that converts websocket request to REST request.  VERY limited at the moment.
	WebsocketToREST(msg sonos.WebsocketRequest) []byte

	// Debug hackery to send a command over a websocket.
	CommandOverWebsocket(id string, namespace string, command string) ([]byte, error)
}

type websocketUser struct {
	hash string
	ws   WebsocketClient
	mqtt mqtt.Client
	data WebDataInterface
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
			bytes, err := data.CommandOverWebsocket(mux.Vars(r)["id"], mux.Vars(r)["namespace"], mux.Vars(r)["command"])
			writeResponse(w, &bytes, err)
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
		hash: hash,
		ws:   nil,
		mqtt: nil,
		data: data,
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
}

func (user *websocketUser) OnClose(userdata string) {
	log.Infof("wsserver: close: %s", userdata)

	// Kill the MQTT client

	// Make sure we remove reference to the MQTT client and websocket
	user.mqtt = nil
	user.ws = nil

	// Delete the user from the map, which should garbage collect it
	users.mutex.Lock()
	delete(users.users, user.hash)
	users.mutex.Unlock()
}

func (user *websocketUser) OnError(userdata string, err error) {
	log.Infof("wsserver: error: %s: %s", userdata, err.Error())
}

func (user *websocketUser) OnMessage(userdata string, bytes []byte) {
	log.Infof("wsserver: message: %s: %s", userdata, string(bytes))

	msg := sonos.WebsocketRequest{}
	if err := msg.FromRawBytes(bytes); err != nil {
		// FIXME: Return a global error here?
		log.Errorf("wsserver: msg error: %s", err.Error())
		user.ws.SendMessage([]byte(err.Error()))
		return
	}

	// Pull out subscribes and use the MQTT client to subscribe.  This is the point
	// where I wish I had stashed the namespace in the MQTT topic, but screw it.  All
	// MQTT events can be a clean slate.
	if msg.Headers.Command == "subscribe" {
		log.Info("subscribe: %s", msg.Headers.Topic)

		// Do we even have a client?
		if user.mqtt == nil {
			var err error = nil
			if user.mqtt, err = initMQTTClient(false); err != nil {
				user.mqtt = nil
				user.ws.SendMessage([]byte(err.Error())) // KLUDGE: return globalError here!
				return
			}

		}
		log.Infof("wsserver: good topic, and haz client: %s", msg.Headers.Topic)
		user.mqtt.Subscribe(msg.Headers.Topic, 0, func(client mqtt.Client, msg mqtt.Message) {
			if user.ws != nil {
				user.ws.SendMessage(msg.Payload())
			}
		})
		return
	}

	// Send it along and reply when we get a response from the player
	//
	// NOTE: We currently do NOT support any command starting with get.  We can, but we need a
	//       lookup table to map from websocket to REST.  Commands "just work" since they always
	//       map directly to REST without having to know stuff only known at codegen time.
	if strings.HasPrefix(msg.Headers.Command, "get") {
		log.Infof("wsserver: NYI: %s:%s", msg.Headers.Namespace, msg.Headers.Command)
	}

	go func() {
		user.ws.SendMessage(user.data.WebsocketToREST(msg))
	}()
}
