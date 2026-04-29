package logchain

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Entry is a single entry in the cryptographic log chain.
// Each entry commits to the previous one — tampering breaks the chain.
type Entry struct {
	// Sequential index — gaps indicate deletion attempts
	Index    uint64    `json:"index"`
	// UUIDv7 of the message this log entry covers
	MessageID uuid.UUID `json:"message_id"`
	// Unix milliseconds — from the message, not the log system
	Timestamp int64     `json:"timestamp"`
	// SHA-256 of the previous entry's hash — links the chain
	PrevHash []byte    `json:"prev_hash"`
	// SHA-256(index || message_id || timestamp || prev_hash || content_hash)
	Hash     []byte    `json:"hash"`
	// Ed25519 signature over Hash — proves the server signed it
	Signature []byte   `json:"signature"`
	// SHA-256 of the message content — content itself is not stored in the chain
	ContentHash []byte `json:"content_hash"`
}

// Chain manages the append-only log chain for a comS.
type Chain struct {
	mu         sync.Mutex
	lastEntry  *Entry
	nextIndex  uint64
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
}

var ErrChainBroken = errors.New("log chain integrity check failed")

// New creates a new Chain from a hex-encoded Ed25519 private key.
func New(secretKeyHex string) (*Chain, error) {
	keyBytes, err := hex.DecodeString(secretKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode secret key: %w", err)
	}

	// Ed25519 private key is 64 bytes: 32 byte seed + 32 byte public key
	if len(keyBytes) != 64 {
		return nil, fmt.Errorf("invalid key length: expected 64 bytes, got %d", len(keyBytes))
	}

	privKey := ed25519.PrivateKey(keyBytes)
	pubKey := privKey.Public().(ed25519.PublicKey)

	return &Chain{
		privateKey: privKey,
		publicKey:  pubKey,
		nextIndex:  0,
	}, nil
}

// NewWithGenesis creates a Chain starting from a persisted genesis entry.
// Used when loading an existing chain from storage on startup.
func NewWithGenesis(secretKeyHex string, lastEntry *Entry, nextIndex uint64) (*Chain, error) {
	c, err := New(secretKeyHex)
	if err != nil {
		return nil, err
	}
	c.lastEntry = lastEntry
	c.nextIndex = nextIndex
	return c, nil
}

// Append adds a new entry to the chain and returns it.
// Thread-safe — multiple goroutines can call this concurrently.
func (c *Chain) Append(messageID uuid.UUID, timestamp time.Time, content []byte) (*Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	prevHash := make([]byte, 32) // Genesis: all zeros
	if c.lastEntry != nil {
		prevHash = c.lastEntry.Hash
	}

	contentHash := sha256Hash(content)
	index := c.nextIndex
	ts := timestamp.UnixMilli()

	// Hash = SHA-256(index || message_id || timestamp || prev_hash || content_hash)
	entryHash := computeHash(index, messageID, ts, prevHash, contentHash)

	// Sign the hash with the server's Ed25519 key
	sig := ed25519.Sign(c.privateKey, entryHash)

	entry := &Entry{
		Index:       index,
		MessageID:   messageID,
		Timestamp:   ts,
		PrevHash:    prevHash,
		Hash:        entryHash,
		Signature:   sig,
		ContentHash: contentHash,
	}

	c.lastEntry = entry
	c.nextIndex++

	return entry, nil
}

// Verify checks the integrity of a slice of entries.
// Returns ErrChainBroken if any entry has been tampered with.
func Verify(entries []*Entry, publicKeyHex string) error {
	keyBytes, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	pubKey := ed25519.PublicKey(keyBytes)

	var prevHash []byte

	for i, entry := range entries {
		// 1. Check index is sequential (no gaps = no deleted entries)
		if uint64(i) != entry.Index {
			return fmt.Errorf("%w: index gap at position %d (expected %d, got %d)",
				ErrChainBroken, i, i, entry.Index)
		}

		// 2. Check prev_hash links correctly
		expectedPrev := make([]byte, 32)
		if i > 0 {
			expectedPrev = entries[i-1].Hash
		}
		if !equalBytes(entry.PrevHash, expectedPrev) {
			return fmt.Errorf("%w: prev_hash mismatch at index %d", ErrChainBroken, i)
		}
		_ = prevHash

		// 3. Recompute hash and verify it matches
		expected := computeHash(entry.Index, entry.MessageID, entry.Timestamp, entry.PrevHash, entry.ContentHash)
		if !equalBytes(entry.Hash, expected) {
			return fmt.Errorf("%w: hash mismatch at index %d", ErrChainBroken, i)
		}

		// 4. Verify Ed25519 signature
		if !ed25519.Verify(pubKey, entry.Hash, entry.Signature) {
			return fmt.Errorf("%w: invalid signature at index %d", ErrChainBroken, i)
		}

		prevHash = entry.Hash
	}

	return nil
}

// VerifyMessage checks that a specific message's content matches its log entry.
// Returns nil if the content is intact, ErrChainBroken if it was tampered with.
func VerifyMessage(content []byte, entry *Entry) error {
	contentHash := sha256Hash(content)
	if !equalBytes(contentHash, entry.ContentHash) {
		return fmt.Errorf("%w: content hash mismatch for message %s",
			ErrChainBroken, entry.MessageID)
	}
	return nil
}

// LastHash returns the hash of the most recently appended entry.
// Returns a zero hash if the chain is empty.
func (c *Chain) LastHash() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastEntry == nil {
		return make([]byte, 32)
	}
	return c.lastEntry.Hash
}

// NextIndex returns the index that will be assigned to the next entry.
func (c *Chain) NextIndex() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nextIndex
}

// PublicKeyHex returns the server's Ed25519 public key as a hex string.
// This is included in the manifest so clients can verify the chain.
func (c *Chain) PublicKeyHex() string {
	return hex.EncodeToString(c.publicKey)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func computeHash(index uint64, messageID uuid.UUID, ts int64, prevHash, contentHash []byte) []byte {
	h := sha256.New()

	// index (8 bytes, big-endian)
	indexBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(indexBytes, index)
	h.Write(indexBytes)

	// message_id (16 bytes)
	msgBytes, _ := messageID.MarshalBinary()
	h.Write(msgBytes)

	// timestamp (8 bytes, big-endian)
	tsBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(tsBytes, uint64(ts))
	h.Write(tsBytes)

	// prev_hash (32 bytes)
	h.Write(prevHash)

	// content_hash (32 bytes)
	h.Write(contentHash)

	return h.Sum(nil)
}

func sha256Hash(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
