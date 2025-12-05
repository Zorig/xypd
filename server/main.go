package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type SignalingMessage struct {
	Type    string          `json:"type"`              // "offer", "answer", "candidate", "join", "error"
	Payload json.RawMessage `json:"payload,omitempty"` // SDP or Candidate
	RoomID  string          `json:"roomId,omitempty"`
}

type Room struct {
	Peers map[*websocket.Conn]bool
	Mutex sync.Mutex
}

var (
	rooms   = make(map[string]*Room)
	roomsMu sync.Mutex
)

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}
	defer conn.Close()

	var currentRoomID string

	for {
		var msg SignalingMessage
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Println("Read error:", err)
			break
		}

		switch msg.Type {
		case "join":
			if joinRoom(conn, msg.RoomID) {
				currentRoomID = msg.RoomID
			}
		default:
			if currentRoomID != "" {
				broadcastToRoom(conn, currentRoomID, msg)
			}
		}
	}

	if currentRoomID != "" {
		leaveRoom(conn, currentRoomID)
	}
}

func joinRoom(conn *websocket.Conn, roomID string) bool {
	roomsMu.Lock()
	defer roomsMu.Unlock()

	room, exists := rooms[roomID]
	if !exists {
		room = &Room{
			Peers: make(map[*websocket.Conn]bool),
		}
		rooms[roomID] = room
	}

	room.Mutex.Lock()
	room.Peers[conn] = true
	peerCount := len(room.Peers)
	room.Mutex.Unlock()

	log.Printf("Peer joined room %s. Total peers: %d\n", roomID, peerCount)

	ackMsg := SignalingMessage{
		Type:   "joined",
		RoomID: roomID,
	}
	conn.WriteJSON(ackMsg)

	// Notify other peers
	if peerCount > 1 {
		notifyMsg := SignalingMessage{
			Type:   "peer_joined",
			RoomID: roomID,
		}
		// We need to broadcast this to everyone EXCEPT the current conn
		// But broadcastToRoom is not available here easily because we are holding the lock?
		// No, we released the lock.
		// But broadcastToRoom iterates all peers.

		// Let's manually iterate here since we are in joinRoom and just released lock.
		// Re-acquire lock to be safe or just use broadcastToRoom helper if we can.
		// broadcastToRoom takes sender *websocket.Conn to exclude.

		// We can call broadcastToRoom.
		go broadcastToRoom(conn, roomID, notifyMsg)
	}

	return true
}

func leaveRoom(conn *websocket.Conn, roomID string) {
	roomsMu.Lock()
	defer roomsMu.Unlock()

	room, exists := rooms[roomID]
	if !exists {
		return
	}

	room.Mutex.Lock()
	delete(room.Peers, conn)
	peerCount := len(room.Peers)
	room.Mutex.Unlock()

	log.Printf("Peer left room %s. Total peers: %d\n", roomID, peerCount)

	if peerCount == 0 {
		delete(rooms, roomID)
		log.Printf("Room %s deleted\n", roomID)
	}
}

func broadcastToRoom(sender *websocket.Conn, roomID string, msg SignalingMessage) {
	roomsMu.Lock()
	room, exists := rooms[roomID]
	roomsMu.Unlock()

	if !exists {
		return
	}

	room.Mutex.Lock()
	defer room.Mutex.Unlock()

	for peer := range room.Peers {
		if peer != sender {
			err := peer.WriteJSON(msg)
			if err != nil {
				log.Printf("Write error to peer in room %s: %v\n", roomID, err)
				peer.Close()
				delete(room.Peers, peer)
			}
		}
	}
}

func main() {
	http.HandleFunc("/ws", handleWebSocket)

	publicDir := "./public"
	if _, err := os.Stat(publicDir); os.IsNotExist(err) {
		publicDir = "../public"
	}
	fs := http.FileServer(http.Dir(publicDir))
	http.Handle("/", fs)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Signaling server started on :%s", port)
	err := http.ListenAndServe(":"+port, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
