# Configuration

All configuration is done via environment variables. Copy `.env.example` to `.env` and fill in the values.

## Required

| Variable | Description |
|----------|-------------|
| `SERVER_ID` | UUID assigned by Central when you registered this server |
| `CENTRAL_API_KEY` | API key from Central (shown once on registration) |
| `CENTRAL_PUBLIC_KEY` | Central's Ed25519 public key — used to verify user tokens offline |
| `SERVER_SECRET_KEY` | This comS's Ed25519 private key — used to sign log chain entries |
| `SERVER_PUBLIC_KEY` | This comS's Ed25519 public key — included in the manifest |

Generate your server keypair with:
```bash
go run cmd/keygen/main.go
```

## Optional

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_NAME` | `My Zync Server` | Display name |
| `PORT` | `3000` | HTTP/WebSocket port |
| `ENV` | `dev` | Environment (`dev` or `prod`) |
| `LOG_LEVEL` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `CENTRAL_URL` | `http://localhost:8080` | Zync Central base URL |
| `DB_PATH` | `./data/messages.db` | SQLite database path |
| `MAX_HISTORY_PER_REQUEST` | `100` | Max messages returned per history request |
| `FEATURE_TEXT_CHANNELS` | `true` | Enable text channels |
| `FEATURE_ANNOUNCEMENT_CHANNELS` | `true` | Enable announcement channels |
| `FEATURE_LOG_CHAIN` | `true` | Enable cryptographic log chain |
| `MAX_CONNECTIONS` | `0` | Max concurrent WebSocket connections (0 = unlimited) |
