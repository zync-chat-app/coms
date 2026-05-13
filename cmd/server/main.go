package main

import (
	"context"
	"errors"
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
	"github.com/zync-chat-app/coms/internal/cli"
	"github.com/zync-chat-app/coms/internal/config"
	"github.com/zync-chat-app/coms/internal/logchain"
	"github.com/zync-chat-app/coms/internal/logger"
	"github.com/zync-chat-app/coms/internal/manifest"
	"github.com/zync-chat-app/coms/internal/storage"
	"github.com/zync-chat-app/coms/internal/ws"
	"go.uber.org/zap"
)

const version = "0.2.0"

func main() {
	envFile := flag.String("env", ".env", "Path to environment file")
	flag.Parse()

	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load(*envFile) // Loads the env file config
	if err != nil {                   // Errors if it's not found or poorly configured
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		fmt.Fprintln(os.Stderr, "Please configure the environment variables file and run again")
		config.GenerateKeys()                // Generates the keys for the owner to add to the .env file
		err := cli.OpenFileForEdit(*envFile) // Opens the env file for edition
		if err != nil {
			return
		}
		fmt.Fprintln(os.Stderr, "Please run the server again to apply the changes")
		os.Exit(1)
	}

	// ── Logger ────────────────────────────────────────────────────────────────
	if err := logger.Init(cfg.LogLevel, "COMS"); err != nil { // Tries to initialize the logger using the log level from the config
		fmt.Fprintf(os.Stderr, "Logger init failed: %v\n", err) // Errors if it can't initialize
		os.Exit(1)
	}

	// Defer logger sync with graceful handling of stdout/stderr sync errors
	// (attempting to sync stdout/stderr returns "invalid argument" on Unix systems)
	defer func(L *zap.Logger) {
		err := L.Sync()
		if err != nil {
			// Ignore "sync /dev/stdout: invalid argument" errors — this is expected
			// when the logger writes to stdout/stderr, as these special files don't support sync
			if err.Error() != "sync /dev/stdout: invalid argument" &&
				err.Error() != "sync /dev/stderr: invalid argument" {
				fmt.Fprintf(os.Stderr, "Logger sync error: %v\n", err)
			}
		}
	}(logger.L)

	// Useless init message, but cool
	fmt.Fprintln(os.Stdout, `
                               ████████
                              ██░░░░░░ 
  █████   ██████  ██████████ ░██       
 ██░░░██ ██░░░░██░░██░░██░░██░█████████
░██  ░░ ░██   ░██ ░██ ░██ ░██░░░░░░░░██
░██   ██░██   ░██ ░██ ░██ ░██       ░██
░░█████ ░░██████  ███ ░██ ░██ ████████ 
 ░░░░░   ░░░░░░  ░░░  ░░  ░░ ░░░░░░░░  
`)

	logger.Info("Starting Zync comS Reference",
		zap.String("server_id", cfg.ServerID),
		zap.String("name", cfg.ServerName),
		zap.String("version", version),
		zap.String("env", cfg.Env),
	)

	// ── SQLite ────────────────────────────────────────────────────────────────
	db, err := storage.Open(cfg.Storage.DBPath) // Tries to load the database
	if err != nil {                             // And returns a fatal error if it can't.
		// For the future: make server not load database if specific argument is passed
		// This would imply not allowing connections from Zync clients
		// And functions would be limited to certain config operations
		logger.Fatal("Failed to open database", zap.Error(err))
	}

	// Changed defer db.Close() to this for error handling
	defer func(db *storage.DB) {
		err := db.Close()
		if err != nil {
			logger.Error("Failed to close database", zap.Error(err))
		}
	}(db)

	logger.Info("Database ready", zap.String("path", cfg.Storage.DBPath))

	// ── Log Chain ─────────────────────────────────────────────────────────────
	ctx := context.Background()                         // I'm guessing this fetches the context we're working with
	lastIdx, lastHash, err := db.GetLastChainEntry(ctx) // I suppose what this does is load the last log chain entry from the database, to know where to resume from
	if err != nil {
		logger.Fatal("Failed to load log chain state", zap.Error(err))
	}

	var chain *logchain.Chain
	if lastIdx == 0 && len(lastHash) == 32 { // If the index is 0 and the length of the hash is 32, do a fresh start
		chain, err = logchain.New(cfg.Crypto.SecretKeyHex) // by generating a new keychain
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
	} else {
		logger.Info("Central client initialized")
	}

	// ── WebSocket Hub ─────────────────────────────────────────────────────────
	hub := ws.NewHub() // This creates a new hub, I believe

	// ── Channel Manager ───────────────────────────────────────────────────────
	channelMgr := channels.NewManager(db, chain, hub) // I suppose this loads the channel manager

	// Register default channels (configurable, these are just examples)
	defaultChannels := []*storage.Channel{
		{ID: "general", Name: "general", Type: "text", Position: 0},                                       // Is the position index ascendant or descendent?
		{ID: "announcements", Name: "announcements", Type: "announcement", Position: 1, IsReadOnly: true}, // The readonly boolean is gonna have to be replaced soon when there's channel perms
	}

	for _, ch := range defaultChannels { // Iterates through every channel to register
		if err := channelMgr.RegisterChannel(ctx, ch); err != nil { // And registers it
			logger.Warn("Failed to register channel", zap.String("id", ch.ID), zap.Error(err)) // If it fails, error
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
		w.WriteHeader(http.StatusOK)                                   // Returns the 200 status
		fmt.Fprintf(w, `{"status":"ok","server_id":"%s","online":%d}`, // And a JSON body with the server ID and the online count
			cfg.ServerID, hub.OnlineCount())
	})

	// This is what makes the heartbeat not error. This endpoint won't be ever called
	// unless the server is unverified. In which case the comS will listen to itself for heartbeat
	// Now that I think about it, wouldn't this be useful for self check status?
	r.Post("/api/v1/servers/{serverID}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		serverID := chi.URLParam(r, "serverID")
		if serverID != cfg.ServerID {
			http.Error(w, "server id mismatch", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"heartbeat received"}`)
	})

	// Manifest — tells clients what this server can do
	r.Get("/manifest", manifest.Handler(cfg, cfg.Crypto.PublicKeyHex, nil))

	// WebSocket endpoint
	r.Get("/connect", hub.ServeWS(centralClient, cfg.Features.MaxConnections))

	// ── Background: Heartbeat ─────────────────────────────────────────────────
	// Starts a timer that sends a heartbeat every few seconds
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

	// Creates a new thread that listens for any web requests at a specified port.
	go func() {
		logger.Info("comS listening at http://localhost:" + cfg.Port)
		// If the web server returns an error that's not http.ErrServerClosed, will it stop serving web requests?
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("Server error", zap.Error(err))
		}
	}()

	cliCtx, cancelCLI := context.WithCancel(context.Background())
	defer cancelCLI()

	repl := cli.New(os.Stdout)
	repl.Register("help", func(ctx context.Context, args []string) error {
		// Displays help about commands
		fmt.Fprintln(os.Stdout, cli.HelpMessage(args))
		return nil
	})
	repl.Register("envedit", func(ctx context.Context, args []string) error {
		return cli.OpenFileForEdit(*envFile) // Opens the env file in the default editor
	})
	repl.Register("envsee", func(ctx context.Context, args []string) error {
		// Prints a bunch of useful info about the server
		fmt.Fprintf(os.Stdout, "comS version: %s\n"+"Server ID: %s\n"+
			"Server Name: %s\n"+"Server's Public Key: %s\n"+
			"Listening on port %s\n"+"Central URL: %s\n",
			version, cfg.ServerID, cfg.ServerName, cfg.Crypto.PublicKeyHex, cfg.Port, cfg.Central.BaseURL)
		return nil
	})
	repl.Register("envshow", func(ctx context.Context, args []string) error {
		// Prints a bunch of useful info about the server
		fmt.Fprintf(os.Stdout, "comS version: %s\n"+"Server ID: %s\n"+
			"Server Name: %s\n"+"Server's Public Key: %s\n"+
			"Listening on port %s\n"+"Central URL: %s\n",
			version, cfg.ServerID, cfg.ServerName, cfg.Crypto.PublicKeyHex, cfg.Port, cfg.Central.BaseURL)
		return nil
	})
	repl.Register("online", func(ctx context.Context, args []string) error {
		fmt.Fprintf(os.Stdout, "online=%d\n", hub.OnlineCount()) // Shows the online count through the terminal
		return nil
	})
	repl.Register("shutdown", func(ctx context.Context, args []string) error {
		return syscall.Kill(os.Getpid(), syscall.SIGINT) // Starts the shutdown process below
	})

	go repl.Run(cliCtx, os.Stdin)

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
