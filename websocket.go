package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

// WebSocketCallbacks contains a set of callbacks for events.  This gets passed to the websocket
// when it is created.  Note that these are not called back on the goroutine that created
// the websocket.
type WebsocketCallbacks interface {
	OnConnect(userData string)
	OnMessage(userData string, msg []byte)
	OnClose(userData string)
	OnError(userData string, err error)
}

// WebsocketClient is the interface for the actual code that manages the websocket
type WebsocketClient interface {
	SendMessage(data []byte) error
	Close()
	IsRunning() bool
}

func NewWebSocket(url string, userData string, headers http.Header, callbacks WebsocketCallbacks) WebsocketClient {
	ws := &websocketImpl{
		userData:    userData,
		callbacks:   callbacks,
		running:     false,
		runningLock: sync.RWMutex{},
		conn:        &websocket.Conn{},
		sendChan:    make(chan []byte),
	}
	ws.run(url, headers)
	return ws
}

//
// Some config.  Move to yaml?
//
const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 8 * 1024
)

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

//
// Actual implementation
//
type websocketImpl struct {
	userData string

	callbacks WebsocketCallbacks

	running     bool
	runningLock sync.RWMutex

	conn *websocket.Conn

	sendChan chan []byte
}

func (ws *websocketImpl) SendMessage(data []byte) error {
	ws.runningLock.RLock()
	defer ws.runningLock.RUnlock()

	if ws.running {
		ws.sendChan <- []byte(data)
		return nil
	}
	return fmt.Errorf("send while not running")
}

func (ws *websocketImpl) Close() {
	var wasRunning bool

	ws.runningLock.RLock()
	wasRunning = ws.running
	ws.runningLock.RUnlock()

	if wasRunning {
		ws.conn.Close()
	}
}

func (ws *websocketImpl) IsRunning() bool {
	ws.runningLock.RLock()
	defer ws.runningLock.RUnlock()
	return ws.running
}

func (ws *websocketImpl) run(url string, headers http.Header) {
	// No time to untangle the cert mess.  Ignore it.  Ew.
	dialer := *websocket.DefaultDialer
	dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	// Fire up the connection
	conn, _, err := dialer.Dial(url, headers)
	if err != nil {
		log.Errorf("ws: dialer failed")
		ws.callbacks.OnError(ws.userData, err)
		return
	}

	ws.conn = conn
	ws.running = true

	// Fire up the goroutines
	go ws.readGoroutine()
	go ws.writeGoroutine()
}

func (ws *websocketImpl) readGoroutine() {
	// Tell someone we connected
	ws.callbacks.OnConnect(ws.userData)

	// Keep reading until we get an error
	ws.conn.SetReadLimit(maxMessageSize)
	ws.conn.SetReadDeadline(time.Now().Add(pongWait))
	ws.conn.SetPongHandler(func(string) error {
		log.Debugf("ws: %s: pong", ws.userData)
		ws.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := ws.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Errorf("ws: unexpected close")
				ws.callbacks.OnError(ws.userData, err)
			}
			break
		}
		message = bytes.TrimSpace(bytes.Replace(message, newline, space, -1))
		ws.callbacks.OnMessage(ws.userData, message)
	}

	// Make sure the connecton is closed.  It is safe to call on a closed connection
	ws.conn.Close()

	// Make sure all other goroutines don't harass us any more
	ws.runningLock.Lock()
	ws.running = false
	ws.runningLock.Unlock()

	// Stop the write goroutine
	close(ws.sendChan)

	// Tell someone that we're done
	ws.callbacks.OnClose(ws.userData)
}

func (ws *websocketImpl) writeGoroutine() {

	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		ws.conn.Close()
	}()

	for {
		select {
		case message, ok := <-ws.sendChan:
			ws.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				log.Infof("ws: sendChan closed")
				ws.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := ws.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				log.Errorf("ws: next writer failed")
				ws.callbacks.OnError(ws.userData, err)
				return
			}
			w.Write(message)

			// Add queued messages to the current websocket message.
			n := len(ws.sendChan)
			for i := 0; i < n; i++ {
				w.Write(newline)
				message = <-ws.sendChan
				w.Write(message)
			}

			if err := w.Close(); err != nil {
				log.Errorf("ws: close on writer failed")
				ws.callbacks.OnError(ws.userData, err)
				return
			}
		case <-ticker.C:
			log.Debugf("ws: %s: ping", ws.userData)
			ws.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := ws.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Errorf("ws: ping failed")
				ws.callbacks.OnError(ws.userData, err)
				return
			}
		}
	}
}
