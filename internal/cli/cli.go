package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode"

	"github.com/zync-chat-app/coms/internal/logger"
	"go.uber.org/zap"
)

// HelpMessage shows general help about commands
// or help about a specific command
func HelpMessage(args []string) string {
	helpString :=
		`Available commands:
	help [command]     Show this message, or see what a command does
	envedit            Edit the environment variables
	envshow / envsee   Show some environment variables
	online             Show number of online clients
	shutdown           Shutdown the comS server
`

	if len(args) == 1 {
		switch args[0] {
		case "envedit":
			helpString = "Opens the default editor to edit the .env file. Requires $EDITOR or $VISUAL environment variable to be set, or a supported OS (Linux, macOS, Windows) to launch the editor"
		case "envshow", "envsee":
			helpString = "Shows the current environment variables relevant to the comS server, such as server ID, server name, and central URL. Does not show sensitive variables"
		case "new":
			helpString = "Creates a new channel. Usage: new <channel_name> [--announcement] [--private]. Example: new \"General Chat\" --announcement"
		case "online":
			helpString = "Displays the number of currently connected clients to the comS server"
		case "shutdown":
			helpString = "Shuts down the comS server, disconnecting all clients and cleaning up resources"
		case "help":
			helpString = "Why would you ever want help about the help command?"
		default:
			helpString = "Unknown command: " + args[0]
		}
	} else if len(args) > 1 {
		helpString = "This command only accepts one argument"
	}

	return helpString
}

type Handler func(ctx context.Context, args []string) error

type REPL struct {
	handlers map[string]Handler
	out      io.Writer
	log      *zap.Logger
}

func New(out io.Writer) *REPL {
	return &REPL{
		handlers: map[string]Handler{},
		out:      out,
		log:      logger.Named("CLI"),
	}
}

func (r *REPL) Register(name string, h Handler) {
	r.handlers[strings.ToLower(strings.TrimSpace(name))] = h
}

func (r *REPL) Run(ctx context.Context, in io.Reader) {
	sc := bufio.NewScanner(in)
	r.log.Info("CLI ready. Type 'help' to see available commands")
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		// Splits the arguments nicely
		parts := splitArgs(line)
		if len(parts) == 0 {
			continue
		}
		cmd := strings.ToLower(parts[0])
		args := parts[1:]

		r.log.Debug("Received command", zap.String("command", cmd), zap.Any("args", args))

		h, ok := r.handlers[cmd]
		if !ok {
			r.log.Warn("Unknown command: " + cmd)
			continue
		}
		if err := h(ctx, args); err != nil {
			r.log.Error(cmd + " error: " + err.Error())
		}
	}
}

// splitArgs splits a command line into arguments honoring single and
// double-quotes as well as backslash escapes. It's a simple lexer
// sufficient for CLI commands (not a full shell parser).
func splitArgs(s string) []string {
	var args []string
	var cur strings.Builder
	inQuote := false
	var quoteChar rune
	escaped := false

	for _, r := range s {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if inQuote {
			if r == quoteChar {
				inQuote = false
				continue
			}
			cur.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			inQuote = true
			quoteChar = r
			continue
		}
		if unicode.IsSpace(r) {
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

// OpenFileForEdit opens a specified file for edition
func OpenFileForEdit(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" { // If the EDITOR variable is empty, try to get the VISUAL variable
		editor = os.Getenv("VISUAL")
	}
	if editor != "" { // If the variable ends up set, execute this
		cmd := exec.Command(editor, path)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// If it ends up not set, run this instead
	// In theory comS only runs on Linux, but why not add support for the rest?
	switch runtime.GOOS {
	case "linux":
		return exec.Command("xdg-open", path).Run()
	case "darwin":
		return exec.Command("open", path).Run()
	case "windows":
		return exec.Command("cmd", "/c", "start", "", path).Run()
	default:
		return fmt.Errorf("no supported editor launcher for %s", runtime.GOOS)
	}
}
