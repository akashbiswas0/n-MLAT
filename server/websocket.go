package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Hub struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]bool
}

var hub = &Hub{
	clients: make(map[*websocket.Conn]bool),
}


func Broadcast(msg interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	var dead []*websocket.Conn
	for conn := range hub.clients {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Printf("WebSocket write error (dropping client): %v", err)
			dead = append(dead, conn)
		}
	}
	for _, conn := range dead {
		delete(hub.clients, conn)
		conn.Close()
	}
}

func HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	hub.mu.Lock()
	hub.clients[conn] = true
	hub.mu.Unlock()

	log.Printf("Browser client connected. Total: %d", len(hub.clients))

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			hub.mu.Lock()
			delete(hub.clients, conn)
			hub.mu.Unlock()
			conn.Close()
			log.Printf("Browser client disconnected. Total: %d", len(hub.clients))
			return
		}
	}
}

func Start(addr string, staticDir string) {
	http.HandleFunc("/ws", HandleWS)
	http.Handle("/", http.FileServer(http.Dir(staticDir)))
	log.Printf("Map UI available at http://localhost%s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("HTTP server error: %v", err)
	}
}
