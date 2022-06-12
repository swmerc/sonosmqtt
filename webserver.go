package main

import (
	"fmt"
	"io"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"

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
		}).Methods("GET")

		router.HandleFunc("/api/v1/group/{id}", func(w http.ResponseWriter, r *http.Request) {
			bytes, err := data.GetGroup(mux.Vars(r)["id"])
			writeResponse(w, &bytes, err)
		}).Methods("GET")

		router.HandleFunc("/api/v1/players", func(w http.ResponseWriter, r *http.Request) {
			bytes, err := data.GetPlayers()
			writeResponse(w, &bytes, err)
		}).Methods("GET")

		router.HandleFunc("/api/v1/player/{id}", func(w http.ResponseWriter, r *http.Request) {
			bytes, err := data.GetPlayer(mux.Vars(r)["id"])
			writeResponse(w, &bytes, err)
		}).Methods("GET")

		//
		// Commands that return unfiltered Sonos responses.  There is some magic mapping going on under
		// the covers, so you can pass the of any player in the group to get group information.
		//
		router.HandleFunc("/api/v1/player/{id}/{namespace}", func(w http.ResponseWriter, r *http.Request) {
			bytes, err := data.GetDataREST(mux.Vars(r)["id"], mux.Vars(r)["namespace"], "")
			writeResponse(w, &bytes, err)
		}).Methods("GET")

		router.HandleFunc("/api/v1/player/{id}/{namespace}/{command}", func(w http.ResponseWriter, r *http.Request) {
			bytes, err := data.GetDataREST(mux.Vars(r)["id"], mux.Vars(r)["namespace"], mux.Vars(r)["command"])
			writeResponse(w, &bytes, err)
		}).Methods("GET")

		router.HandleFunc("/api/v1/player/{id}/{namespace}/{command}", func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			bytes := make([]byte, 0)
			if err == nil {
				bytes, err = data.PostDataREST(mux.Vars(r)["id"], mux.Vars(r)["namespace"], mux.Vars(r)["command"], body)
			}
			writeResponse(w, &bytes, err)
		}).Methods("POST")

		//
		// Websocket that can take Sonos control API commands and return events.  Wooo?
		//

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
