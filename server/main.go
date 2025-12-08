package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"
)

// Configuration - can be overridden via environment variables
var config = struct {
	MaxPeersPerRoom  int
	RoomCleanupDelay time.Duration
	MaxConnections   int64
	RateLimitPerSec  float64
	RateLimitBurst   int
	AllowedOrigins   []string
}{
	MaxPeersPerRoom:  getEnvInt("MAX_PEERS_PER_ROOM", 2),
	RoomCleanupDelay: getEnvDuration("ROOM_CLEANUP_DELAY", 30*time.Minute),
	MaxConnections:   int64(getEnvInt("MAX_CONNECTIONS", 1000)),
	RateLimitPerSec:  float64(getEnvInt("RATE_LIMIT_PER_SEC", 10)),
	RateLimitBurst:   getEnvInt("RATE_LIMIT_BURST", 20),
	AllowedOrigins:   getEnvList("ALLOWED_ORIGINS", []string{}),
}

// Helper functions for config
func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		n := 0
		for _, c := range val {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			} else {
				return defaultVal
			}
		}
		if len(val) > 0 {
			return n
		}
	}
	return defaultVal
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultVal
}

func getEnvList(key string, defaultVal []string) []string {
	if val := os.Getenv(key); val != "" {
		return strings.Split(val, ",")
	}
	return defaultVal
}

// Connection tracking
var (
	activeConnections int64
	rateLimiter       = rate.NewLimiter(rate.Limit(10), 20) // Updated in init()
)

func init() {
	// Apply config to rate limiter
	rateLimiter = rate.NewLimiter(rate.Limit(config.RateLimitPerSec), config.RateLimitBurst)
}

// CORS origin checker
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")

	// Always allow empty origin (same-origin requests)
	if origin == "" {
		return true
	}

	// Allow localhost for development
	if strings.HasPrefix(origin, "http://localhost") ||
		strings.HasPrefix(origin, "https://localhost") ||
		strings.HasPrefix(origin, "http://127.0.0.1") ||
		strings.HasPrefix(origin, "https://127.0.0.1") {
		return true
	}

	// Check against configured allowed origins
	if len(config.AllowedOrigins) > 0 {
		for _, allowed := range config.AllowedOrigins {
			if origin == strings.TrimSpace(allowed) {
				return true
			}
		}
		// If origins are configured but none match, reject
		slog.Warn("Origin rejected", "origin", origin)
		return false
	}

	// No origins configured = allow all (development mode)
	return true
}

var upgrader = websocket.Upgrader{
	CheckOrigin: checkOrigin,
}

type SignalingMessage struct {
	Type    string          `json:"type"`              // "offer", "answer", "candidate", "join", "error"
	Payload json.RawMessage `json:"payload,omitempty"` // SDP or Candidate
	RoomID  string          `json:"roomId,omitempty"`
}

type Room struct {
	Peers     map[*websocket.Conn]bool
	Mutex     sync.Mutex
	CreatedAt time.Time
}

var (
	rooms   = make(map[string]*Room)
	roomsMu sync.RWMutex
)

// Rate limiting middleware
func rateLimitMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !rateLimiter.Allow() {
			slog.Warn("Rate limit exceeded", "remoteAddr", r.RemoteAddr)
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Check connection limit
	currentConns := atomic.LoadInt64(&activeConnections)
	if currentConns >= config.MaxConnections {
		slog.Warn("Connection limit reached",
			"current", currentConns,
			"max", config.MaxConnections,
			"remoteAddr", r.RemoteAddr,
		)
		http.Error(w, "Server at capacity", http.StatusServiceUnavailable)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket upgrade failed", "error", err)
		return
	}

	// Track connection
	atomic.AddInt64(&activeConnections, 1)
	defer func() {
		atomic.AddInt64(&activeConnections, -1)
		conn.Close()
	}()

	// Set write deadline to prevent slow consumers from blocking
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))

	// Log connection info for simple analytics
	userAgent := r.Header.Get("User-Agent")
	origin := r.Header.Get("Origin")
	remoteAddr := r.RemoteAddr

	slog.Info("New connection",
		"remoteAddr", remoteAddr,
		"origin", origin,
		"userAgent", userAgent,
		"activeConnections", atomic.LoadInt64(&activeConnections),
	)

	var currentRoomID string

	for {
		var msg SignalingMessage
		err := conn.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Warn("WebSocket read error", "error", err)
			}
			break
		}

		switch msg.Type {
		case "join":
			if joinRoom(conn, msg.RoomID) {
				currentRoomID = msg.RoomID
			}
		case "offer":
			// Log when a transfer is initiated (offer = sender starting)
			slog.Info("Transfer initiated",
				"roomID", msg.RoomID,
				"origin", origin,
				"remoteAddr", remoteAddr,
			)
			if currentRoomID != "" {
				broadcastToRoom(conn, currentRoomID, msg)
			}
		default:
			if currentRoomID != "" {
				broadcastToRoom(conn, currentRoomID, msg)
			}
		}
	}

	if currentRoomID != "" {
		leaveRoom(conn, currentRoomID)
		slog.Info("Connection closed",
			"roomID", currentRoomID,
			"remoteAddr", remoteAddr,
			"activeConnections", atomic.LoadInt64(&activeConnections)-1,
		)
	}
}

func joinRoom(conn *websocket.Conn, roomID string) bool {
	roomsMu.Lock()
	defer roomsMu.Unlock()

	room, exists := rooms[roomID]
	if !exists {
		room = &Room{
			Peers:     make(map[*websocket.Conn]bool),
			CreatedAt: time.Now(),
		}
		rooms[roomID] = room
	}

	room.Mutex.Lock()
	defer room.Mutex.Unlock()

	// Check room size limit
	if len(room.Peers) >= config.MaxPeersPerRoom {
		slog.Warn("Room full, rejecting peer", "roomID", roomID, "currentPeers", len(room.Peers))
		errorMsg := SignalingMessage{
			Type:    "error",
			Payload: json.RawMessage(`"Room is full. Only 2 peers allowed."`),
			RoomID:  roomID,
		}
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteJSON(errorMsg); err != nil {
			slog.Error("Failed to send room full error", "error", err)
		}
		return false
	}

	room.Peers[conn] = true
	peerCount := len(room.Peers)

	slog.Info("Peer joined room", "roomID", roomID, "peerCount", peerCount)

	ackMsg := SignalingMessage{
		Type:   "joined",
		RoomID: roomID,
	}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(ackMsg); err != nil {
		slog.Error("Failed to send join acknowledgment", "error", err, "roomID", roomID)
		delete(room.Peers, conn)
		return false
	}

	// Notify other peers
	if peerCount > 1 {
		notifyMsg := SignalingMessage{
			Type:   "peer_joined",
			RoomID: roomID,
		}
		for peer := range room.Peers {
			if peer != conn {
				peer.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := peer.WriteJSON(notifyMsg); err != nil {
					slog.Error("Failed to notify peer", "error", err, "roomID", roomID)
				}
			}
		}
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

	slog.Info("Peer left room", "roomID", roomID, "remainingPeers", peerCount)

	if peerCount == 0 {
		delete(rooms, roomID)
		slog.Info("Room deleted", "roomID", roomID)
	}
}

func broadcastToRoom(sender *websocket.Conn, roomID string, msg SignalingMessage) {
	roomsMu.RLock()
	room, exists := rooms[roomID]
	if !exists {
		roomsMu.RUnlock()
		return
	}

	room.Mutex.Lock()
	roomsMu.RUnlock()
	defer room.Mutex.Unlock()

	for peer := range room.Peers {
		if peer != sender {
			peer.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := peer.WriteJSON(msg); err != nil {
				slog.Error("Failed to broadcast to peer", "error", err, "roomID", roomID)
				peer.Close()
				delete(room.Peers, peer)
			}
		}
	}
}

func cleanupStaleRooms(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			roomsMu.Lock()
			now := time.Now()
			for roomID, room := range rooms {
				room.Mutex.Lock()
				if len(room.Peers) == 0 && now.Sub(room.CreatedAt) > config.RoomCleanupDelay {
					delete(rooms, roomID)
					slog.Info("Cleaned up stale room", "roomID", roomID)
				}
				room.Mutex.Unlock()
			}
			roomsMu.Unlock()
		}
	}
}

func main() {
	// Setup structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Log configuration on startup
	slog.Info("Server configuration",
		"maxConnections", config.MaxConnections,
		"maxPeersPerRoom", config.MaxPeersPerRoom,
		"rateLimitPerSec", config.RateLimitPerSec,
		"rateLimitBurst", config.RateLimitBurst,
		"allowedOrigins", config.AllowedOrigins,
	)

	// Setup graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start room cleanup goroutine
	go cleanupStaleRooms(ctx)

	// Apply rate limiting to WebSocket endpoint
	http.HandleFunc("/ws", rateLimitMiddleware(handleWebSocket))

	// Health check endpoint with stats
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		roomsMu.RLock()
		roomCount := len(rooms)
		roomsMu.RUnlock()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":            "healthy",
			"roomCount":         roomCount,
			"activeConnections": atomic.LoadInt64(&activeConnections),
			"maxConnections":    config.MaxConnections,
			"timestamp":         time.Now().UTC().Format(time.RFC3339),
		})
	})

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

	server := &http.Server{
		Addr:         ":" + port,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		slog.Info("Signaling server started", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal
	<-ctx.Done()
	slog.Info("Shutting down server...")

	// Graceful shutdown with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("Server shutdown failed", "error", err)
	}

	slog.Info("Server stopped gracefully")
}
