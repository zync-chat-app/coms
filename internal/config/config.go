package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	// Server identity
	ServerID   string // UUID assigned by Central on registration
	ServerName string // Display name
	Port       string
	Env        string
	LogLevel   string

	// Central connection
	Central CentralConfig

	// Crypto
	Crypto CryptoConfig

	// Storage
	Storage StorageConfig

	// Features
	Features FeatureConfig
}

type CentralConfig struct {
	// Base URL of the Zync Central server
	BaseURL string // e.g. "https://central.zync.app" or "http://localhost:8080"
	// API key issued by Central when this server was registered
	APIKey  string
	// Zync Central's Ed25519 public key — used to verify scoped tokens offline
	PublicKeyHex string
}

type CryptoConfig struct {
	// Ed25519 private key of this comS — used to sign log chain entries
	// Generated once with: go run cmd/keygen/main.go
	SecretKeyHex string
	PublicKeyHex string
}

type StorageConfig struct {
	// Path to the SQLite database file
	DBPath string // default: "./data/messages.db"
	// Maximum message history to serve per request
	MaxHistoryPerRequest int // default: 100
}

type FeatureConfig struct {
	// Which built-in channel types to enable
	EnableTextChannels        bool
	EnableAnnouncementChannels bool
	// Whether to enforce the log chain (required for Verified servers)
	EnableLogChain bool
	// Maximum concurrent WebSocket connections
	MaxConnections int // 0 = unlimited
}

func Load(envFile string) (*Config, error) {
	if envFile != "" {
		if err := godotenv.Load(envFile); err != nil {
			return nil, fmt.Errorf("could not load %s: %w", envFile, err)
		}
	}

	cfg := &Config{
		ServerID:   os.Getenv("SERVER_ID"),
		ServerName: getEnvOrDefault("SERVER_NAME", "My Zync Server"),
		Port:       getEnvOrDefault("PORT", "3000"),
		Env:        getEnvOrDefault("ENV", "dev"),
		LogLevel:   getEnvOrDefault("LOG_LEVEL", "info"),

		Central: CentralConfig{
			BaseURL:      getEnvOrDefault("CENTRAL_URL", "http://localhost:8080"),
			APIKey:       os.Getenv("CENTRAL_API_KEY"),
			PublicKeyHex: os.Getenv("CENTRAL_PUBLIC_KEY"),
		},

		Crypto: CryptoConfig{
			SecretKeyHex: os.Getenv("SERVER_SECRET_KEY"),
			PublicKeyHex: os.Getenv("SERVER_PUBLIC_KEY"),
		},

		Storage: StorageConfig{
			DBPath:               getEnvOrDefault("DB_PATH", "./data/messages.db"),
			MaxHistoryPerRequest: getEnvInt("MAX_HISTORY_PER_REQUEST", 100),
		},

		Features: FeatureConfig{
			EnableTextChannels:         getEnvBool("FEATURE_TEXT_CHANNELS", true),
			EnableAnnouncementChannels: getEnvBool("FEATURE_ANNOUNCEMENT_CHANNELS", true),
			EnableLogChain:             getEnvBool("FEATURE_LOG_CHAIN", true),
			MaxConnections:             getEnvInt("MAX_CONNECTIONS", 0),
		},
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	type check struct {
		value  string
		envVar string
	}

	required := []check{
		{c.ServerID,            "SERVER_ID"},
		{c.Central.BaseURL,     "CENTRAL_URL"},
		{c.Central.APIKey,      "CENTRAL_API_KEY"},
		{c.Central.PublicKeyHex,"CENTRAL_PUBLIC_KEY"},
		{c.Crypto.SecretKeyHex, "SERVER_SECRET_KEY"},
		{c.Crypto.PublicKeyHex, "SERVER_PUBLIC_KEY"},
	}

	var missing []string
	for _, r := range required {
		if strings.TrimSpace(r.value) == "" {
			missing = append(missing, r.envVar)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf(
			"missing required environment variables: %s\n  → run: go run cmd/keygen/main.go",
			strings.Join(missing, ", "),
		)
	}

	return nil
}

// IsDev returns true in development mode — enables extra logging and relaxed checks.
func (c *Config) IsDev() bool {
	return c.Env == "dev"
}

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// Validate returns a combined error if any required fields are missing.
var ErrMissingConfig = errors.New("missing required configuration")
