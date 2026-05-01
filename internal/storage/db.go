package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

// DB wraps the SQLite connection with comS-specific queries.
type DB struct {
	sql *sql.DB
}

// Message is a stored message in a channel.
type Message struct {
	ID        uuid.UUID `json:"id"`
	ChannelID string    `json:"channel_id"`
	UserID    uuid.UUID `json:"user_id"`
	Content   string    `json:"content"`
	// Metadata: reactions, attachments, embeds etc. as JSON
	Metadata string `json:"metadata"`
	// Edit history: JSON array of previous content versions
	EditHistory []string `json:"edit_history,omitempty"`
	// Soft-delete: content is cleared but entry remains for log chain
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`

	// Log chain fields
	ChainIndex uint64 `json:"chain_index"`
	ChainHash  []byte `json:"chain_hash"`
}

// Channel is a registered channel on this comS.
type Channel struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Type       string    `json:"type"` // "text" | "announcement" | "forum"
	Topic      string    `json:"topic,omitempty"`
	IsReadOnly bool      `json:"is_read_only"`
	Position   int       `json:"position"`
	CreatedAt  time.Time `json:"created_at"`
}

// Open initializes the SQLite database, creating the file and schema if needed.
func Open(path string) (*DB, error) {
	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	// Enable WAL mode for better concurrent read/write performance
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on", path)
	sqlDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Connection pool: SQLite handles concurrency via WAL, not connections
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	db := &DB{sql: sqlDB}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func (db *DB) Close() error {
	return db.sql.Close()
}

// ─── Schema ───────────────────────────────────────────────────────────────────

func (db *DB) migrate() error {
	_, err := db.sql.Exec(`
		CREATE TABLE IF NOT EXISTS channels (
			id          TEXT        PRIMARY KEY,
			name        TEXT        NOT NULL,
			type        TEXT        NOT NULL DEFAULT 'text',
			topic       TEXT        NOT NULL DEFAULT '',
			is_read_only INTEGER    NOT NULL DEFAULT 0,
			position    INTEGER     NOT NULL DEFAULT 0,
			created_at  INTEGER     NOT NULL  -- unix ms
		);

		CREATE TABLE IF NOT EXISTS messages (
			id           TEXT    PRIMARY KEY,   -- UUIDv7
			channel_id   TEXT    NOT NULL REFERENCES channels(id),
			user_id      TEXT    NOT NULL,       -- UUID from Central
			content      TEXT    NOT NULL DEFAULT '',
			metadata     TEXT    NOT NULL DEFAULT '{}',
			edit_history TEXT    NOT NULL DEFAULT '[]',
			deleted_at   INTEGER,               -- unix ms, NULL = not deleted
			created_at   INTEGER NOT NULL,      -- unix ms
			updated_at   INTEGER NOT NULL,      -- unix ms

			-- Log chain
			chain_index  INTEGER NOT NULL,
			chain_hash   BLOB    NOT NULL
		);

		-- Full-text search on message content
		CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
			content,
			content='messages',
			content_rowid='rowid'
		);

		-- Trigger: keep FTS in sync on insert
		CREATE TRIGGER IF NOT EXISTS messages_fts_insert
		AFTER INSERT ON messages BEGIN
			INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
		END;

		-- Trigger: keep FTS in sync on update
		CREATE TRIGGER IF NOT EXISTS messages_fts_update
		AFTER UPDATE ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.rowid, old.content);
			INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
		END;

		-- Trigger: keep FTS in sync on delete
		CREATE TRIGGER IF NOT EXISTS messages_fts_delete
		AFTER DELETE ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.rowid, old.content);
		END;

		CREATE TABLE IF NOT EXISTS log_chain (
			idx         INTEGER PRIMARY KEY,
			message_id  TEXT    NOT NULL,
			timestamp   INTEGER NOT NULL,
			prev_hash   BLOB    NOT NULL,
			hash        BLOB    NOT NULL UNIQUE,
			signature   BLOB    NOT NULL,
			content_hash BLOB   NOT NULL
		);

		CREATE TABLE IF NOT EXISTS members (
			user_id     TEXT    NOT NULL,
			joined_at   INTEGER NOT NULL,
			last_seen   INTEGER NOT NULL,
			metadata    TEXT    NOT NULL DEFAULT '{}',
			PRIMARY KEY (user_id)
		);

		CREATE INDEX IF NOT EXISTS idx_messages_channel  ON messages(channel_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_messages_user     ON messages(user_id);
		CREATE INDEX IF NOT EXISTS idx_log_chain_msg     ON log_chain(message_id);
	`)
	return err
}

// ─── Channels ─────────────────────────────────────────────────────────────────

func (db *DB) UpsertChannel(ctx context.Context, ch *Channel) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO channels (id, name, type, topic, is_read_only, position, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name        = excluded.name,
			topic       = excluded.topic,
			is_read_only = excluded.is_read_only,
			position    = excluded.position
	`,
		ch.ID, ch.Name, ch.Type, ch.Topic,
		boolToInt(ch.IsReadOnly), ch.Position,
		ch.CreatedAt.UnixMilli(),
	)
	return err
}

func (db *DB) ListChannels(ctx context.Context) ([]*Channel, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, type, topic, is_read_only, position, created_at
		FROM channels ORDER BY position -- ASC is the default ordering
	`)
	if err != nil {
		return nil, err
	}
	defer func(rows *sql.Rows) {
		err := rows.Close()
		if err != nil {
			// Error handling here
		}
	}(rows)

	var channels []*Channel
	for rows.Next() {
		var ch Channel
		var createdMs int64
		var isReadOnly int
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.Type, &ch.Topic,
			&isReadOnly, &ch.Position, &createdMs); err != nil {
			continue
		}
		ch.IsReadOnly = isReadOnly == 1
		ch.CreatedAt = time.UnixMilli(createdMs).UTC()
		channels = append(channels, &ch)
	}
	return channels, nil
}

// ─── Messages ─────────────────────────────────────────────────────────────────

func (db *DB) InsertMessage(ctx context.Context, m *Message) error {
	editHistoryJSON, _ := json.Marshal(m.EditHistory)

	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO messages (
			id, channel_id, user_id, content, metadata,
			edit_history, created_at, updated_at,
			chain_index, chain_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		m.ID.String(), m.ChannelID, m.UserID.String(),
		m.Content, m.Metadata, string(editHistoryJSON),
		m.CreatedAt.UnixMilli(), m.UpdatedAt.UnixMilli(),
		m.ChainIndex, m.ChainHash,
	)
	return err
}

// GetHistory returns messages for a channel, newest first, with pagination.
// before: if set, returns only messages older than this message ID (cursor-based pagination)
func (db *DB) GetHistory(ctx context.Context, channelID string, limit int, before *uuid.UUID) ([]*Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	query := `
		SELECT id, channel_id, user_id, content, metadata,
		       edit_history, deleted_at, created_at, updated_at,
		       chain_index, chain_hash
		FROM messages
		WHERE channel_id = ?`

	args := []any{channelID}

	if before != nil {
		query += ` AND created_at < (SELECT created_at FROM messages WHERE id = ?)`
		args = append(args, before.String())
	}

	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}
	defer func(rows *sql.Rows) {
		err := rows.Close()
		if err != nil {
			// Error handling here
		}
	}(rows)

	var messages []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			continue
		}
		messages = append(messages, m)
	}

	return messages, nil
}

// EditMessage updates message content and appends to edit history.
func (db *DB) EditMessage(ctx context.Context, messageID uuid.UUID, userID uuid.UUID, newContent string) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func(tx *sql.Tx) {
		err := tx.Rollback()
		if err != nil {
			// Error handling here
		}
	}(tx)

	// Get current content for history
	var oldContent string
	var editHistoryJSON string
	err = tx.QueryRowContext(ctx,
		`SELECT content, edit_history FROM messages WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		messageID.String(), userID.String(),
	).Scan(&oldContent, &editHistoryJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("message not found or not owned by user")
	}
	if err != nil {
		return err
	}

	var history []string
	json.Unmarshal([]byte(editHistoryJSON), &history)
	history = append(history, oldContent) // Save old content to history

	newHistoryJSON, _ := json.Marshal(history)
	now := time.Now().UnixMilli()

	_, err = tx.ExecContext(ctx, `
		UPDATE messages
		SET content = ?, edit_history = ?, updated_at = ?
		WHERE id = ?
	`, newContent, string(newHistoryJSON), now, messageID.String())
	if err != nil {
		return err
	}

	return tx.Commit()
}

// SoftDeleteMessage clears content but keeps the entry for log chain integrity.
func (db *DB) SoftDeleteMessage(ctx context.Context, messageID uuid.UUID, userID uuid.UUID) error {
	now := time.Now().UnixMilli()
	tag, err := db.sql.ExecContext(ctx, `
		UPDATE messages
		SET content = '[deleted]', metadata = '{}', deleted_at = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, now, now, messageID.String(), userID.String())
	if err != nil {
		return err
	}
	n, _ := tag.RowsAffected()
	if n == 0 {
		return fmt.Errorf("message not found or not owned by user")
	}
	return nil
}

// Search performs full-text search on message content in a channel.
func (db *DB) Search(ctx context.Context, channelID, query string, limit int) ([]*Message, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	rows, err := db.sql.QueryContext(ctx, `
		SELECT m.id, m.channel_id, m.user_id, m.content, m.metadata,
		       m.edit_history, m.deleted_at, m.created_at, m.updated_at,
		       m.chain_index, m.chain_hash
		FROM messages m
		JOIN messages_fts fts ON m.rowid = fts.rowid
		WHERE m.channel_id = ?
		  AND fts.content MATCH ?
		  AND m.deleted_at IS NULL
		ORDER BY m.created_at DESC
		LIMIT ?
	`, channelID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer func(rows *sql.Rows) {
		err := rows.Close()
		if err != nil {
			// Error handling here
		}
	}(rows)

	var messages []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			continue
		}
		messages = append(messages, m)
	}
	return messages, nil
}

// ─── Members ──────────────────────────────────────────────────────────────────

func (db *DB) UpsertMember(ctx context.Context, userID uuid.UUID) error {
	now := time.Now().UnixMilli()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO members (user_id, joined_at, last_seen)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET last_seen = excluded.last_seen
	`, userID.String(), now, now)
	return err
}

func (db *DB) UpdateMemberLastSeen(ctx context.Context, userID uuid.UUID) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE members SET last_seen = ? WHERE user_id = ?
	`, time.Now().UnixMilli(), userID.String())
	return err
}

func (db *DB) MemberCount(ctx context.Context) (int, error) {
	var count int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM members`).Scan(&count)
	return count, err
}

// ─── Log Chain ────────────────────────────────────────────────────────────────

func (db *DB) InsertChainEntry(ctx context.Context, idx uint64, messageID uuid.UUID, ts int64, prevHash, hash, sig, contentHash []byte) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO log_chain (idx, message_id, timestamp, prev_hash, hash, signature, content_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, idx, messageID.String(), ts, prevHash, hash, sig, contentHash)
	return err
}

func (db *DB) GetLastChainEntry(ctx context.Context) (idx uint64, hash []byte, err error) {
	err = db.sql.QueryRowContext(ctx, `
		SELECT idx, hash FROM log_chain ORDER BY idx DESC LIMIT 1
	`).Scan(&idx, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, make([]byte, 32), nil
	}
	return
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func scanMessage(rows *sql.Rows) (*Message, error) {
	var m Message
	var idStr, channelID, userIDStr string
	var editHistoryJSON string
	var createdMs, updatedMs int64
	var deletedMs *int64

	err := rows.Scan(
		&idStr, &channelID, &userIDStr,
		&m.Content, &m.Metadata, &editHistoryJSON,
		&deletedMs, &createdMs, &updatedMs,
		&m.ChainIndex, &m.ChainHash,
	)
	if err != nil {
		return nil, err
	}

	m.ID, _ = uuid.Parse(idStr)
	m.ChannelID = channelID
	m.UserID, _ = uuid.Parse(userIDStr)
	m.CreatedAt = time.UnixMilli(createdMs).UTC()
	m.UpdatedAt = time.UnixMilli(updatedMs).UTC()

	if deletedMs != nil {
		t := time.UnixMilli(*deletedMs).UTC()
		m.DeletedAt = &t
	}

	json.Unmarshal([]byte(editHistoryJSON), &m.EditHistory)

	return &m, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
