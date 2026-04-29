package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/zync-chat-app/coms/internal/logchain"
	"github.com/zync-chat-app/coms/internal/storage"
	"github.com/zync-chat-app/coms/internal/ws"
)

const (
	maxMessageLength = 4000 // characters
	maxHistoryLimit  = 100
)

// Manager handles all channel operations and registers WebSocket handlers.
type Manager struct {
	db    *storage.DB
	chain *logchain.Chain
	hub   *ws.Hub
}

func NewManager(db *storage.DB, chain *logchain.Chain, hub *ws.Hub) *Manager {
	m := &Manager{db: db, chain: chain, hub: hub}
	m.registerHandlers()
	return m
}

// RegisterChannel creates or updates a channel in the database.
// Called on startup to ensure configured channels exist.
func (m *Manager) RegisterChannel(ctx context.Context, ch *storage.Channel) error {
	if ch.CreatedAt.IsZero() {
		ch.CreatedAt = time.Now().UTC()
	}
	return m.db.UpsertChannel(ctx, ch)
}

// ─── Handler Registration ─────────────────────────────────────────────────────

func (m *Manager) registerHandlers() {
	m.hub.Register("zync.channels.list",            m.handleList)
	m.hub.Register("zync.channels.message.create",  m.handleCreate)
	m.hub.Register("zync.channels.message.history", m.handleHistory)
	m.hub.Register("zync.channels.message.edit",    m.handleEdit)
	m.hub.Register("zync.channels.message.delete",  m.handleDelete)
	m.hub.Register("zync.channels.search",          m.handleSearch)
	m.hub.Register("zync.channels.typing.start",    m.handleTypingStart)
	m.hub.Register("zync.channels.typing.stop",     m.handleTypingStop)
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

// handleList returns all channels on this server.
func (m *Manager) handleList(ctx context.Context, client *ws.Client, msg *ws.Message) (*ws.Message, error) {
	channels, err := m.db.ListChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}

	return respond("zync.channels.list.ok", msg.ID, map[string]any{
		"channels": channels,
	}), nil
}

// handleCreate processes a new message from a client.
func (m *Manager) handleCreate(ctx context.Context, client *ws.Client, msg *ws.Message) (*ws.Message, error) {
	var req struct {
		ChannelID string `json:"channel_id"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal(msg.D, &req); err != nil {
		return nil, errors.New("invalid payload")
	}

	if req.ChannelID == "" {
		return nil, errors.New("channel_id is required")
	}
	if req.Content == "" {
		return nil, errors.New("content is required")
	}
	if utf8.RuneCountInString(req.Content) > maxMessageLength {
		return nil, fmt.Errorf("message too long (max %d characters)", maxMessageLength)
	}

	// Verify channel exists
	channels, err := m.db.ListChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("verify channel: %w", err)
	}
	var channel *storage.Channel
	for _, ch := range channels {
		if ch.ID == req.ChannelID {
			channel = ch
			break
		}
	}
	if channel == nil {
		return nil, fmt.Errorf("channel %q not found", req.ChannelID)
	}
	if channel.IsReadOnly {
		return nil, errors.New("this channel is read-only")
	}

	// Build message
	msgID, _ := uuid.NewV7()
	now := time.Now().UTC()

	message := &storage.Message{
		ID:        msgID,
		ChannelID: req.ChannelID,
		UserID:    client.UserID,
		Content:   req.Content,
		Metadata:  "{}",
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Append to log chain
	entry, err := m.chain.Append(msgID, now, []byte(req.Content))
	if err != nil {
		return nil, fmt.Errorf("log chain: %w", err)
	}
	message.ChainIndex = entry.Index
	message.ChainHash = entry.Hash

	// Store in SQLite
	if err := m.db.InsertMessage(ctx, message); err != nil {
		return nil, fmt.Errorf("store message: %w", err)
	}

	// Store chain entry
	m.db.InsertChainEntry(ctx,
		entry.Index, entry.MessageID, entry.Timestamp,
		entry.PrevHash, entry.Hash, entry.Signature, entry.ContentHash,
	)

	// Broadcast to all connected clients
	broadcast := &ws.Message{
		T:  "zync.channels.message.create",
		ID: newMsgID(),
		D: mustJSON(map[string]any{
			"id":          msgID,
			"channel_id":  req.ChannelID,
			"user_id":     client.UserID,
			"content":     req.Content,
			"chain_index": entry.Index,
			"created_at":  now,
		}),
	}
	m.hub.Broadcast(broadcast)

	// Return ack to sender only
	return respond("zync.channels.message.create.ok", msg.ID, map[string]any{
		"id":          msgID,
		"chain_index": entry.Index,
	}), nil
}

// handleHistory returns message history for a channel.
func (m *Manager) handleHistory(ctx context.Context, client *ws.Client, msg *ws.Message) (*ws.Message, error) {
	var req struct {
		ChannelID string  `json:"channel_id"`
		Limit     int     `json:"limit"`
		Before    *string `json:"before"` // message UUID cursor
	}
	if err := json.Unmarshal(msg.D, &req); err != nil {
		return nil, errors.New("invalid payload")
	}

	if req.ChannelID == "" {
		return nil, errors.New("channel_id is required")
	}

	if req.Limit <= 0 || req.Limit > maxHistoryLimit {
		req.Limit = 50
	}

	var before *uuid.UUID
	if req.Before != nil {
		id, err := uuid.Parse(*req.Before)
		if err != nil {
			return nil, errors.New("invalid before cursor")
		}
		before = &id
	}

	messages, err := m.db.GetHistory(ctx, req.ChannelID, req.Limit, before)
	if err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}

	return respond("zync.channels.message.history", msg.ID, map[string]any{
		"channel_id": req.ChannelID,
		"messages":   messages,
		"has_more":   len(messages) == req.Limit,
	}), nil
}

// handleEdit edits a message (owner only).
func (m *Manager) handleEdit(ctx context.Context, client *ws.Client, msg *ws.Message) (*ws.Message, error) {
	var req struct {
		MessageID string `json:"message_id"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal(msg.D, &req); err != nil {
		return nil, errors.New("invalid payload")
	}

	if req.MessageID == "" || req.Content == "" {
		return nil, errors.New("message_id and content are required")
	}

	msgID, err := uuid.Parse(req.MessageID)
	if err != nil {
		return nil, errors.New("invalid message_id")
	}

	if utf8.RuneCountInString(req.Content) > maxMessageLength {
		return nil, fmt.Errorf("message too long (max %d characters)", maxMessageLength)
	}

	if err := m.db.EditMessage(ctx, msgID, client.UserID, req.Content); err != nil {
		return nil, err
	}

	// Broadcast edit to all clients
	m.hub.Broadcast(&ws.Message{
		T:  "zync.channels.message.edit",
		ID: newMsgID(),
		D: mustJSON(map[string]any{
			"message_id": msgID,
			"content":    req.Content,
			"edited_at":  time.Now().UTC(),
		}),
	})

	return respond("zync.channels.message.edit.ok", msg.ID, nil), nil
}

// handleDelete soft-deletes a message (owner only).
func (m *Manager) handleDelete(ctx context.Context, client *ws.Client, msg *ws.Message) (*ws.Message, error) {
	var req struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(msg.D, &req); err != nil {
		return nil, errors.New("invalid payload")
	}

	msgID, err := uuid.Parse(req.MessageID)
	if err != nil {
		return nil, errors.New("invalid message_id")
	}

	if err := m.db.SoftDeleteMessage(ctx, msgID, client.UserID); err != nil {
		return nil, err
	}

	m.hub.Broadcast(&ws.Message{
		T:  "zync.channels.message.delete",
		ID: newMsgID(),
		D:  mustJSON(map[string]any{"message_id": msgID}),
	})

	return respond("zync.channels.message.delete.ok", msg.ID, nil), nil
}

// handleSearch performs full-text search.
func (m *Manager) handleSearch(ctx context.Context, client *ws.Client, msg *ws.Message) (*ws.Message, error) {
	var req struct {
		ChannelID string `json:"channel_id"`
		Query     string `json:"query"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal(msg.D, &req); err != nil {
		return nil, errors.New("invalid payload")
	}

	if req.Query == "" {
		return nil, errors.New("query is required")
	}

	messages, err := m.db.Search(ctx, req.ChannelID, req.Query, req.Limit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	return respond("zync.channels.search.results", msg.ID, map[string]any{
		"query":    req.Query,
		"messages": messages,
	}), nil
}

// handleTypingStart broadcasts a typing indicator.
func (m *Manager) handleTypingStart(ctx context.Context, client *ws.Client, msg *ws.Message) (*ws.Message, error) {
	var req struct {
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal(msg.D, &req); err != nil {
		return nil, errors.New("invalid payload")
	}

	m.hub.BroadcastExcept(&ws.Message{
		T:  "zync.channels.typing.start",
		ID: newMsgID(),
		D: mustJSON(map[string]string{
			"channel_id": req.ChannelID,
			"user_id":    client.UserID.String(),
		}),
	}, client.UserID)

	return nil, nil // no ack needed
}

// handleTypingStop broadcasts end of typing indicator.
func (m *Manager) handleTypingStop(ctx context.Context, client *ws.Client, msg *ws.Message) (*ws.Message, error) {
	var req struct {
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal(msg.D, &req); err != nil {
		return nil, errors.New("invalid payload")
	}

	m.hub.BroadcastExcept(&ws.Message{
		T:  "zync.channels.typing.stop",
		ID: newMsgID(),
		D: mustJSON(map[string]string{
			"channel_id": req.ChannelID,
			"user_id":    client.UserID.String(),
		}),
	}, client.UserID)

	return nil, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func respond(t, refID string, data any) *ws.Message {
	return &ws.Message{
		T:  t,
		ID: newMsgID(),
		D:  mustJSON(data),
	}
}

func newMsgID() string {
	id, _ := uuid.NewV7()
	return id.String()
}

func mustJSON(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage("null")
	}
	data, _ := json.Marshal(v)
	return data
}
