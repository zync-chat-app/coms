package central

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"aidanwoods.dev/go-paseto"
	"github.com/google/uuid"
	"github.com/zync-chat-app/coms/internal/config"
	"github.com/zync-chat-app/coms/internal/logger"
	"go.uber.org/zap"
)

// Client handles all communication with Zync Central.
type Client struct {
	cfg    *config.Config
	http   *http.Client
	log    *zap.Logger
	pubKey paseto.V4AsymmetricPublicKey
}

// ScopedTokenClaims are the claims extracted from a user's scoped token.
type ScopedTokenClaims struct {
	UserID    uuid.UUID
	ServerID  uuid.UUID
	IssuedAt  time.Time
	ExpiresAt time.Time
}

func New(cfg *config.Config) (*Client, error) {
	keyBytes, err := hex.DecodeString(cfg.Central.PublicKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode central public key: %w", err)
	}

	pubKey, err := paseto.NewV4AsymmetricPublicKeyFromBytes(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse central public key: %w", err)
	}

	return &Client{
		cfg:    cfg,
		http:   &http.Client{Timeout: 10 * time.Second},
		log:    logger.Named("CENTRAL"),
		pubKey: pubKey,
	}, nil
}

// VerifyScopedToken validates a user's scoped PASETO token offline.
// No network call needed — the token is verified against Central's public key.
// This is intentional: if Central is down, existing users can still chat.
func (c *Client) VerifyScopedToken(tokenStr string) (*ScopedTokenClaims, error) {
	parser := paseto.NewParser()
	parser.AddRule(paseto.NotExpired())

	token, err := parser.ParseV4Public(c.pubKey, tokenStr, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid scoped token: %w", err)
	}

	// Verify this token is meant for this server
	tokenType, _ := token.GetString("type")
	if tokenType != "scoped_server" {
		return nil, errors.New("wrong token type: expected scoped_server")
	}

	serverIDStr, _ := token.GetString("server_id")
	serverID, err := uuid.Parse(serverIDStr)
	if err != nil {
		return nil, errors.New("invalid server_id claim")
	}

	// Reject tokens issued for a different server
	expectedServerID, err := uuid.Parse(c.cfg.ServerID)
	if err != nil {
		return nil, fmt.Errorf("invalid SERVER_ID in config: %w", err)
	}
	if serverID != expectedServerID {
		return nil, fmt.Errorf("token is for server %s, not %s", serverID, expectedServerID)
	}

	userIDStr, err := token.GetSubject()
	if err != nil {
		return nil, errors.New("missing sub claim")
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return nil, errors.New("invalid sub claim")
	}

	iat, _ := token.GetIssuedAt()
	exp, _ := token.GetExpiration()

	return &ScopedTokenClaims{
		UserID:    userID,
		ServerID:  serverID,
		IssuedAt:  iat,
		ExpiresAt: exp,
	}, nil
}

// SendHeartbeat notifies Central that this comS is alive.
// Called every 60 seconds by a background goroutine.
func (c *Client) SendHeartbeat(ctx context.Context, softwareVersion string) error {
	url := fmt.Sprintf("%s/api/v1/servers/%s/heartbeat",
		c.cfg.Central.BaseURL, c.cfg.ServerID)

	body := fmt.Sprintf(`{"software_version":"%s"}`, softwareVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body)) // There's no need for a custom string reader
	if err != nil {
		return fmt.Errorf("build heartbeat request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.Central.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			// Error handling here
		}
	}(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("heartbeat rejected: invalid or revoked API key")
	}
	if resp.StatusCode == http.StatusForbidden {
		return errors.New("heartbeat rejected: server is suspended or terminated")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("heartbeat failed with status %d", resp.StatusCode)
	}

	return nil
}

// RunHeartbeat starts the background heartbeat loop.
// Logs errors but does not stop — temporary Central downtime should not kill the comS.
func (c *Client) RunHeartbeat(ctx context.Context, version string) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	// Send immediately on startup
	if err := c.SendHeartbeat(ctx, version); err != nil {
		c.log.Warn("initial heartbeat failed", zap.Error(err))
	} else {
		c.log.Info("heartbeat sent", zap.String("central", c.cfg.Central.BaseURL))
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.SendHeartbeat(ctx, version); err != nil {
				c.log.Warn("heartbeat failed", zap.Error(err))
			}
		}
	}
}
