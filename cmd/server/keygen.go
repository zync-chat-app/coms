//go:build ignore

// Run with: go run cmd/server/keygen.go
// Generates an Ed25519 keypair for this comS.
// Add the output to your .env file.

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stdout, "🔑 Zync comS Key Generator")
	fmt.Fprintln(os.Stdout, "──────────────────────────────────")
	fmt.Fprintln(os.Stdout, "Add the output below to your .env file.")
	fmt.Fprintln(os.Stdout, "Never commit SERVER_SECRET_KEY to version control.")
	fmt.Fprintln(os.Stdout, "──────────────────────────────────")
	fmt.Println()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Ed25519 private key in Go is 64 bytes: seed (32) + public key (32)
	fmt.Printf("SERVER_SECRET_KEY=%s\n", hex.EncodeToString(priv))
	fmt.Printf("SERVER_PUBLIC_KEY=%s\n", hex.EncodeToString(pub))

	fmt.Println()
	fmt.Fprintln(os.Stdout, "✅ Done. Keep SERVER_SECRET_KEY secret!")
}
