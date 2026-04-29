package manifest

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/zync-chat-app/coms/internal/config"
)

const ProtocolVersion = "1.0"
const SoftwareVersion = "0.1.0"

// Manifest is served at GET /manifest.
// It declares what this comS can do — clients use this to render the UI.
// NOTE: Trust information (verified, status) comes from Central, NOT from here.
type Manifest struct {
	// Protocol version — client checks this for compatibility
	ProtocolVersion string `json:"protocol_version"`
	// Software version of this comS implementation
	SoftwareVersion string `json:"software_version"`
	// Server ID — same as the one registered with Central
	ServerID string `json:"server_id"`
	// Human-readable name (informational only)
	ServerName string `json:"server_name"`

	// Capabilities: which official extensions are enabled
	Capabilities []string `json:"capabilities"`

	// Extensions: custom extensions this server supports
	// Each extension can provide its own UI and message namespace
	Extensions []Extension `json:"extensions"`

	// Ed25519 public key of this comS
	// Clients use this to verify the log chain
	PublicKey string `json:"public_key"`

	// When this manifest was generated
	GeneratedAt time.Time `json:"generated_at"`
}

// Extension describes a custom extension supported by this comS.
type Extension struct {
	// Reverse-domain identifier (e.g. "com.example.chess")
	ID string `json:"id"`
	// Semver version string
	Version string `json:"version"`
	// Human-readable name
	Name string `json:"name"`
	// Optional sandboxed UI URL
	// If present, client will offer to load this in a sandboxed frame
	UIURL *string `json:"ui_url,omitempty"`
	// Which WebSocket message namespaces this extension uses
	// e.g. ["com.example.chess.*"]
	MessageNamespaces []string `json:"message_namespaces"`
}

// Handler returns an http.HandlerFunc that serves the manifest.
// Cache-Control is set by the handler — clients should respect it.
func Handler(cfg *config.Config, publicKeyHex string, extensions []Extension) http.HandlerFunc {
	capabilities := buildCapabilities(cfg)

	manifest := &Manifest{
		ProtocolVersion: ProtocolVersion,
		SoftwareVersion: SoftwareVersion,
		ServerID:        cfg.ServerID,
		ServerName:      cfg.ServerName,
		Capabilities:    capabilities,
		Extensions:      extensions,
		PublicKey:       publicKeyHex,
	}

	if manifest.Extensions == nil {
		manifest.Extensions = []Extension{}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		manifest.GeneratedAt = time.Now().UTC()

		data, err := json.Marshal(manifest)
		if err != nil {
			http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
			return
		}

		// Clients should re-fetch the manifest occasionally.
		// 5 minutes is a good default — fast enough to pick up changes,
		// slow enough not to hammer the server.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}
}

func buildCapabilities(cfg *config.Config) []string {
	var caps []string

	if cfg.Features.EnableTextChannels {
		caps = append(caps, "channels")
	}
	if cfg.Features.EnableAnnouncementChannels {
		caps = append(caps, "announcements")
	}
	if cfg.Features.EnableLogChain {
		caps = append(caps, "log_chain")
	}

	// Always supported
	caps = append(caps,
		"typing_indicators",
		"message_history",
		"message_search",
		"message_edit",
		"message_delete",
	)

	return caps
}
