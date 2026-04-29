package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/zync-chat-app/coms/internal/central"
	"github.com/zync-chat-app/coms/internal/logger"
	"go.uber.org/zap"
)

// Message is the canonical wire format for all WebSocket messages.
// t = type (namespace.action), id = UUIDv7 for dedup, d = data payload
type Message struct {
	T  string          `json:"t"`
	ID string          `json:"id"`
	D  json.RawMessage `json:"d,omitempty"`
	TS int64           `json:"ts"` // unix ms, set by server
}

// MessageHandler processes an incoming WebSocket message from a client.
// Returns a response message (or nil if no response needed) and an error.
type MessageHandler func(ctx context.Context, client *Client, msg *Message) (*Message, error)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		// TODO: in prod, validate against allowed origins
		return true
	},
}

// ─── Hub ──────────────────────────────────────────────────────────────────────

// Hub manages all active WebSocket connections.
type Hub struct {
	mu       sync.RWMutex
	clients  map[uuid.UUID]*Client  // userID → client
	handlers map[string]MessageHandler
	log      *zap.Logger
}

func NewHub() *Hub {
	return &Hub{
		clients:  make(map[uuid.UUID]*Client),
		handlers: make(map[string]MessageHandler),
		log:      logger.Named("WS"),
	}
}

// Register adds a message handler for a specific message type.
// e.g. hub.Register("zync.channels.message.create", handleCreate)
func (h *Hub) Register(msgType string, handler MessageHandler) {
	h.handlers[msgType] = handler
}

// Broadcast sends a message to all connected clients.
func (h *Hub) Broadcast(msg *Message) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	for _, client := range h.clients {
		client.send(data)
	}
}

// BroadcastExcept sends to all clients except the specified user.
func (h *Hub) BroadcastExcept(msg *Message, excludeUserID uuid.UUID) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	for userID, client := range h.clients {
		if userID != excludeUserID {
			client.send(data)
		}
	}
}

// Send sends a message to a specific user.
func (h *Hub) Send(userID uuid.UUID, msg *Message) {
	h.mu.RLock()
	client, ok := h.clients[userID]
	h.mu.RUnlock()

	if !ok {
		return
	}

	data, _ := json.Marshal(msg)
	client.send(data)
}

// OnlineCount returns the number of currently connected users.
func (h *Hub) OnlineCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// IsOnline returns true if a user currently has an active connection.
func (h *Hub) IsOnline(userID uuid.UUID) bool {
	h.mu.RLock()
	_, ok := h.clients[userID]
	h.mu.RUnlock()
	return ok
}

func (h *Hub) addClient(c *Client) {
	h.mu.Lock()
	h.clients[c.UserID] = c
	h.mu.Unlock()
}

func (h *Hub) removeClient(c *Client) {
	h.mu.Lock()
	delete(h.clients, c.UserID)
	h.mu.Unlock()
}

func (h *Hub) dispatch(ctx context.Context, client *Client, msg *Message) {
	handler, ok := h.handlers[msg.T]
	if !ok {
		// Unknown message type — send error back to client only
		client.sendJSON(&Message{
			T:  "zync.core.error",
			ID: newMsgID(),
			TS: nowMS(),
			D:  mustJSON(map[string]string{
				"code":    "unknown_message_type",
				"message": "Unknown message type: " + msg.T,
				"ref":     msg.ID,
			}),
		})
		return
	}

	resp, err := handler(ctx, client, msg)
	if err != nil {
		h.log.Warn("handler error",
			zap.String("type", msg.T),
			zap.String("user", client.UserID.String()),
			zap.Error(err),
		)
		client.sendJSON(&Message{
			T:  "zync.core.error",
			ID: newMsgID(),
			TS: nowMS(),
			D:  mustJSON(map[string]string{
				"code":    "handler_error",
				"message": err.Error(),
				"ref":     msg.ID,
			}),
		})
		return
	}

	if resp != nil {
		client.sendJSON(resp)
	}
}

// ─── ServeWS ─────────────────────────────────────────────────────────────────

// ServeWS upgrades an HTTP connection to WebSocket and manages the client lifecycle.
func (h *Hub) ServeWS(centralClient *central.Client, maxConnections int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Validate scoped token from query string
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, `{"error":"missing_token"}`, http.StatusUnauthorized)
			return
		}

		claims, err := centralClient.VerifyScopedToken(token)
		if err != nil {
			h.log.Warn("invalid scoped token", zap.Error(err))
			http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
			return
		}

		// 2. Check connection limit
		if maxConnections > 0 && h.OnlineCount() >= maxConnections {
			http.Error(w, `{"error":"server_full"}`, http.StatusServiceUnavailable)
			return
		}

		// 3. Upgrade to WebSocket
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			h.log.Error("websocket upgrade failed", zap.Error(err))
			return
		}

		client := newClient(conn, claims.UserID, h)
		h.addClient(client)

		h.log.Info("client connected",
			zap.String("user_id", claims.UserID.String()),
			zap.Int("online", h.OnlineCount()),
		)

		// 4. Send hello message
		client.sendJSON(&Message{
			T:  "zync.core.hello",
			ID: newMsgID(),
			TS: nowMS(),
			D:  mustJSON(map[string]any{
				"user_id":    claims.UserID,
				"session_expires_at": claims.ExpiresAt,
				"online_count": h.OnlineCount(),
			}),
		})

		// 5. Notify others
		h.BroadcastExcept(&Message{
			T:  "zync.core.user.join",
			ID: newMsgID(),
			TS: nowMS(),
			D:  mustJSON(map[string]string{"user_id": claims.UserID.String()}),
		}, claims.UserID)

		// 6. Run client loops
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		go client.writePump()
		client.readPump(ctx, h) // blocks until disconnected

		// 7. Cleanup
		h.removeClient(client)
		h.log.Info("client disconnected",
			zap.String("user_id", claims.UserID.String()),
			zap.Int("online", h.OnlineCount()),
		)

		h.Broadcast(&Message{
			T:  "zync.core.user.leave",
			ID: newMsgID(),
			TS: nowMS(),
			D:  mustJSON(map[string]string{"user_id": claims.UserID.String()}),
		})
	}
}

// ─── Client ───────────────────────────────────────────────────────────────────

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = 50 * time.Second // must be < pongWait
	maxMessageSize = 16 * 1024        // 16 KB per message
)

// Client represents a single WebSocket connection.
type Client struct {
	UserID uuid.UUID
	conn   *websocket.Conn
	hub    *Hub
	sendCh chan []byte
}

func newClient(conn *websocket.Conn, userID uuid.UUID, hub *Hub) *Client {
	return &Client{
		UserID: userID,
		conn:   conn,
		hub:    hub,
		sendCh: make(chan []byte, 256),
	}
}

// readPump reads incoming messages from the WebSocket connection.
func (c *Client) readPump(ctx context.Context, hub *Hub) {
	defer c.conn.Close()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, rawMsg, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure) {
				hub.log.Debug("unexpected close", zap.Error(err))
			}
			return
		}

		var msg Message
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			c.sendJSON(&Message{
				T:  "zync.core.error",
				ID: newMsgID(),
				TS: nowMS(),
				D:  mustJSON(map[string]string{"code": "invalid_json", "message": "Invalid JSON"}),
			})
			continue
		}

		// Handle ping inline — fast path, no handler lookup needed
		if msg.T == "zync.core.ping" {
			c.sendJSON(&Message{T: "zync.core.pong", ID: newMsgID(), TS: nowMS()})
			continue
		}

		hub.dispatch(ctx, c, &msg)
	}
}

// writePump writes outgoing messages to the WebSocket connection.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case data, ok := <-c.sendCh:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) send(data []byte) {
	select {
	case c.sendCh <- data:
	default:
		// Buffer full — client is too slow, disconnect
		close(c.sendCh)
	}
}

func (c *Client) sendJSON(msg *Message) {
	if msg.TS == 0 {
		msg.TS = nowMS()
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	c.send(data)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func nowMS() int64 { return time.Now().UnixMilli() }

func newMsgID() string {
	id, _ := uuid.NewV7()
	return id.String()
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
