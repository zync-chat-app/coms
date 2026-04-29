package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/zync-chat-app/coms/internal/central"
	"github.com/zync-chat-app/coms/internal/channels"
	"github.com/zync-chat-app/coms/internal/config"
	"github.com/zync-chat-app/coms/internal/logchain"
	"github.com/zync-chat-app/coms/internal/logger"
	"github.com/zync-chat-app/coms/internal/manifest"
	"github.com/zync-chat-app/coms/internal/storage"
	"github.com/zync-chat-app/coms/internal/ws"
	"go.uber.org/zap"
)

const version = "0.1.0"

func main() {
	envFile := flag.String("env", ".env", "Path to environment file")
	flag.Parse()

	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load(*envFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		os.Exit(1)
	}

	// ── Logger ────────────────────────────────────────────────────────────────
	if err := logger.Init(cfg.LogLevel, cfg.Env, "COMS"); err != nil {
		fmt.Fprintf(os.Stderr, "Logger init failed: %v\n", err)
		os.Exit(1)
	}
	defer logger.L.Sync()

	logger.Info("Starting Zync comS Reference",
		zap.String("server_id", cfg.ServerID),
		zap.String("name", cfg.ServerName),
		zap.String("version", version),
		zap.String("env", cfg.Env),
	)

	// ── SQLite ────────────────────────────────────────────────────────────────
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		logger.Fatal("Failed to open database", zap.Error(err))
	}
	defer db.Close()
	logger.Info("Database ready", zap.String("path", cfg.Storage.DBPath))

	// ── Log Chain ─────────────────────────────────────────────────────────────
	ctx := context.Background()
	lastIdx, lastHash, err := db.GetLastChainEntry(ctx)
	if err != nil {
		logger.Fatal("Failed to load log chain state", zap.Error(err))
	}

	var chain *logchain.Chain
	if lastIdx == 0 && len(lastHash) == 32 {
		// Fresh start
		chain, err = logchain.New(cfg.Crypto.SecretKeyHex)
	} else {
		// Resume from last known entry
		logger.Info("Resuming log chain", zap.Uint64("next_index", lastIdx+1))
		chain, err = logchain.NewWithGenesis(cfg.Crypto.SecretKeyHex, nil, lastIdx+1)
	}
	if err != nil {
		logger.Fatal("Failed to init log chain", zap.Error(err))
	}

	// ── Central Client ────────────────────────────────────────────────────────
	centralClient, err := central.New(cfg)
	if err != nil {
		logger.Fatal("Failed to init Central client", zap.Error(err))
	}

	// ── WebSocket Hub ─────────────────────────────────────────────────────────
	hub := ws.NewHub()

	// ── Channel Manager ───────────────────────────────────────────────────────
	channelMgr := channels.NewManager(db, chain, hub)

	// Register default channels (configurable, these are just examples)
	defaultChannels := []*storage.Channel{
		{ID: "general", Name: "general", Type: "text", Position: 0},
		{ID: "announcements", Name: "announcements", Type: "announcement", Position: 1, IsReadOnly: true},
	}
	for _, ch := range defaultChannels {
		if err := channelMgr.RegisterChannel(ctx, ch); err != nil {
			logger.Warn("Failed to register channel", zap.String("id", ch.ID), zap.Error(err))
		}
	}
	logger.Info("Channels registered", zap.Int("count", len(defaultChannels)))

	// ── HTTP Router ───────────────────────────────────────────────────────────
	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Recoverer)

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","server_id":"%s","online":%d}`,
			cfg.ServerID, hub.OnlineCount())
	})

	// Manifest — tells clients what this server can do
	r.Get("/manifest", manifest.Handler(cfg, cfg.Crypto.PublicKeyHex, nil))

	// WebSocket endpoint
	r.Get("/connect", hub.ServeWS(centralClient, cfg.Features.MaxConnections))

	// ── Background: Heartbeat ─────────────────────────────────────────────────
	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	defer cancelHeartbeat()
	go centralClient.RunHeartbeat(heartbeatCtx, version)

	// ── HTTP Server ───────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Info("comS listening", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Server error", zap.Error(err))
		}
	}()

	// ── Graceful Shutdown ─────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	logger.Info("Shutdown signal received", zap.String("signal", sig.String()))
	cancelHeartbeat()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("Forced shutdown", zap.Error(err))
	}

	logger.Info("comS stopped. Goodbye 👋")
}
