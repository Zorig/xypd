package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestSignalingMessageParsing tests JSON message parsing
func TestSignalingMessageParsing(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantType  string
		wantRoom  string
		wantError bool
	}{
		{
			name:     "ValidJoinMessage",
			input:    `{"type":"join","roomId":"ABC123"}`,
			wantType: "join",
			wantRoom: "ABC123",
		},
		{
			name:     "ValidOfferMessage",
			input:    `{"type":"offer","roomId":"XYZ789","payload":{"sdp":"v=0...","type":"offer"}}`,
			wantType: "offer",
			wantRoom: "XYZ789",
		},
		{
			name:      "InvalidJSON",
			input:     `{not valid json}`,
			wantError: true,
		},
		{
			name:     "EmptyType",
			input:    `{"type":"","roomId":"TEST"}`,
			wantType: "",
			wantRoom: "TEST",
		},
		{
			name:     "CandidateMessage",
			input:    `{"type":"candidate","roomId":"ROOM1","payload":{"candidate":"a=...","sdpMid":"0","sdpMLineIndex":0}}`,
			wantType: "candidate",
			wantRoom: "ROOM1",
		},
		{
			name:     "AnswerMessage",
			input:    `{"type":"answer","roomId":"ROOM2","payload":{"sdp":"v=0...","type":"answer"}}`,
			wantType: "answer",
			wantRoom: "ROOM2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg SignalingMessage
			err := json.Unmarshal([]byte(tt.input), &msg)

			if tt.wantError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if msg.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", msg.Type, tt.wantType)
			}
			if msg.RoomID != tt.wantRoom {
				t.Errorf("RoomID = %q, want %q", msg.RoomID, tt.wantRoom)
			}
		})
	}
}

// TestRoomDataStructure tests room struct operations without WebSocket
func TestRoomDataStructure(t *testing.T) {
	t.Run("RoomCreation", func(t *testing.T) {
		room := &Room{
			Peers:     make(map[*websocket.Conn]bool),
			CreatedAt: time.Now(),
		}

		if room.Peers == nil {
			t.Error("Peers map should not be nil")
		}
		if len(room.Peers) != 0 {
			t.Error("New room should have 0 peers")
		}
	})

	t.Run("RoomMutexLocking", func(t *testing.T) {
		room := &Room{
			Peers:     make(map[*websocket.Conn]bool),
			CreatedAt: time.Now(),
		}

		// Test that mutex can be locked and unlocked
		room.Mutex.Lock()
		room.Mutex.Unlock()

		// Test with defer pattern
		func() {
			room.Mutex.Lock()
			defer room.Mutex.Unlock()
			// Simulated work
		}()
	})
}

// TestConcurrentMapAccess tests thread safety of rooms map
func TestConcurrentMapAccess(t *testing.T) {
	// Reset global state
	rooms = make(map[string]*Room)

	var wg sync.WaitGroup
	numGoroutines := 50

	// Concurrent reads and writes
	wg.Add(numGoroutines * 2)

	// Writers
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			roomID := "ROOM"
			roomsMu.Lock()
			rooms[roomID] = &Room{
				Peers:     make(map[*websocket.Conn]bool),
				CreatedAt: time.Now(),
			}
			roomsMu.Unlock()
		}(i)
	}

	// Readers
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			roomsMu.RLock()
			_ = rooms["ROOM"]
			roomsMu.RUnlock()
		}(i)
	}

	wg.Wait()
}

// TestHealthEndpoint tests the /health endpoint
func TestHealthEndpoint(t *testing.T) {
	rooms = make(map[string]*Room)

	// Add test rooms
	roomsMu.Lock()
	rooms["HEALTH1"] = &Room{
		Peers:     make(map[*websocket.Conn]bool),
		CreatedAt: time.Now(),
	}
	rooms["HEALTH2"] = &Room{
		Peers:     make(map[*websocket.Conn]bool),
		CreatedAt: time.Now(),
	}
	roomsMu.Unlock()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		roomsMu.RLock()
		roomCount := len(rooms)
		roomsMu.RUnlock()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":    "healthy",
			"roomCount": roomCount,
		})
	})

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response["status"] != "healthy" {
		t.Errorf("Expected status 'healthy', got %v", response["status"])
	}

	if response["roomCount"].(float64) != 2 {
		t.Errorf("Expected roomCount 2, got %v", response["roomCount"])
	}

	// Cleanup
	rooms = make(map[string]*Room)
}

// TestWebSocketIntegration tests real WebSocket connections
func TestWebSocketIntegration(t *testing.T) {
	rooms = make(map[string]*Room)

	server := httptest.NewServer(http.HandlerFunc(handleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	dialer := websocket.Dialer{}

	t.Run("ConnectionUpgrade", func(t *testing.T) {
		conn, resp, err := dialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("WebSocket dial failed: %v", err)
		}
		defer conn.Close()

		if resp.StatusCode != http.StatusSwitchingProtocols {
			t.Errorf("Expected 101, got %d", resp.StatusCode)
		}
	})

	t.Run("JoinRoom", func(t *testing.T) {
		conn, _, err := dialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("WebSocket dial failed: %v", err)
		}
		defer conn.Close()

		// Send join message
		joinMsg := SignalingMessage{Type: "join", RoomID: "TESTROOM"}
		if err := conn.WriteJSON(joinMsg); err != nil {
			t.Fatalf("Failed to send join: %v", err)
		}

		// Read acknowledgment
		var ackMsg SignalingMessage
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := conn.ReadJSON(&ackMsg); err != nil {
			t.Fatalf("Failed to read ack: %v", err)
		}

		if ackMsg.Type != "joined" {
			t.Errorf("Expected 'joined', got %q", ackMsg.Type)
		}
		if ackMsg.RoomID != "TESTROOM" {
			t.Errorf("Expected room 'TESTROOM', got %q", ackMsg.RoomID)
		}
	})

	t.Run("TwoPeersJoin", func(t *testing.T) {
		rooms = make(map[string]*Room) // Reset

		conn1, _, err := dialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("Peer 1 dial failed: %v", err)
		}
		defer conn1.Close()

		conn2, _, err := dialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("Peer 2 dial failed: %v", err)
		}
		defer conn2.Close()

		roomID := "TWOPEERS"

		// Peer 1 joins
		conn1.WriteJSON(SignalingMessage{Type: "join", RoomID: roomID})
		var ack1 SignalingMessage
		conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
		conn1.ReadJSON(&ack1)

		// Peer 2 joins
		conn2.WriteJSON(SignalingMessage{Type: "join", RoomID: roomID})
		var ack2 SignalingMessage
		conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
		conn2.ReadJSON(&ack2)

		// Peer 1 should get peer_joined notification
		var notification SignalingMessage
		conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := conn1.ReadJSON(&notification); err != nil {
			t.Fatalf("Failed to read peer_joined: %v", err)
		}

		if notification.Type != "peer_joined" {
			t.Errorf("Expected 'peer_joined', got %q", notification.Type)
		}
	})

	t.Run("RoomFullRejectsThirdPeer", func(t *testing.T) {
		rooms = make(map[string]*Room) // Reset

		conn1, _, _ := dialer.Dial(wsURL, nil)
		defer conn1.Close()
		conn2, _, _ := dialer.Dial(wsURL, nil)
		defer conn2.Close()
		conn3, _, _ := dialer.Dial(wsURL, nil)
		defer conn3.Close()

		roomID := "FULLROOM"

		// First two peers join
		conn1.WriteJSON(SignalingMessage{Type: "join", RoomID: roomID})
		conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
		var ack1 SignalingMessage
		conn1.ReadJSON(&ack1)

		conn2.WriteJSON(SignalingMessage{Type: "join", RoomID: roomID})
		conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
		var ack2 SignalingMessage
		conn2.ReadJSON(&ack2)

		// Drain peer_joined from conn1
		conn1.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		var peerJoined SignalingMessage
		conn1.ReadJSON(&peerJoined)

		// Third peer should be rejected
		conn3.WriteJSON(SignalingMessage{Type: "join", RoomID: roomID})
		conn3.SetReadDeadline(time.Now().Add(2 * time.Second))
		var errorMsg SignalingMessage
		if err := conn3.ReadJSON(&errorMsg); err != nil {
			t.Fatalf("Failed to read error: %v", err)
		}

		if errorMsg.Type != "error" {
			t.Errorf("Expected 'error', got %q", errorMsg.Type)
		}
	})

	t.Run("MessageRelay", func(t *testing.T) {
		rooms = make(map[string]*Room) // Reset

		conn1, _, _ := dialer.Dial(wsURL, nil)
		defer conn1.Close()
		conn2, _, _ := dialer.Dial(wsURL, nil)
		defer conn2.Close()

		roomID := "RELAY"

		// Both peers join
		conn1.WriteJSON(SignalingMessage{Type: "join", RoomID: roomID})
		conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
		var ack1 SignalingMessage
		conn1.ReadJSON(&ack1)

		conn2.WriteJSON(SignalingMessage{Type: "join", RoomID: roomID})
		conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
		var ack2 SignalingMessage
		conn2.ReadJSON(&ack2)

		// Drain peer_joined from conn1
		conn1.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		var peerJoined SignalingMessage
		conn1.ReadJSON(&peerJoined)

		// Peer 1 sends offer
		offerPayload := json.RawMessage(`{"sdp":"v=0...","type":"offer"}`)
		offer := SignalingMessage{Type: "offer", RoomID: roomID, Payload: offerPayload}
		conn1.WriteJSON(offer)

		// Peer 2 should receive it
		var received SignalingMessage
		conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := conn2.ReadJSON(&received); err != nil {
			t.Fatalf("Failed to receive offer: %v", err)
		}

		if received.Type != "offer" {
			t.Errorf("Expected 'offer', got %q", received.Type)
		}
	})
}

// TestConstants tests server configuration
func TestConstants(t *testing.T) {
	if config.MaxPeersPerRoom != 2 {
		t.Errorf("MaxPeersPerRoom should be 2, got %d", config.MaxPeersPerRoom)
	}

	if config.RoomCleanupDelay < 5*time.Minute {
		t.Error("RoomCleanupDelay too short")
	}

	if config.MaxConnections < 100 {
		t.Error("MaxConnections too low")
	}

	if config.RateLimitPerSec < 1 {
		t.Error("RateLimitPerSec too low")
	}
}

// BenchmarkMessageParsing benchmarks JSON parsing
func BenchmarkMessageParsing(b *testing.B) {
	input := `{"type":"offer","roomId":"BENCH123","payload":{"sdp":"v=0...","type":"offer"}}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var msg SignalingMessage
		json.Unmarshal([]byte(input), &msg)
	}
}
