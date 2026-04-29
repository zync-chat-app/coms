# Zync comS — Reference Implementation

The official reference implementation of a **Zync Community Server (comS)**.

This repository shows how to build a comS that is fully compatible with the Zync protocol. It is intentionally simple and well-documented — use it as a starting point for your own implementation or run it as-is.

> **Trust comes from Central, not from this server.**  
> Verified status, server health, and user identity are all managed by the Zync Central server. This comS cannot claim to be verified on its own.

## Features

- ✅ Full Zync Protocol v1.0 compliance
- ✅ Offline PASETO token verification (works even if Central is temporarily down)
- ✅ Cryptographic log chain — every message is signed and tamper-evident
- ✅ Text channels with full message history
- ✅ Full-text search (SQLite FTS5)
- ✅ Typing indicators
- ✅ Edit history preserved for every message
- ✅ Soft-delete (message content cleared, entry kept for log chain integrity)
- ✅ Automatic heartbeat to Central
- ✅ Extension system — add custom message namespaces

## Quick Start

**Prerequisites:** Go 1.22+, a registered server on Zync Central.

```bash
# 1. Clone
git clone https://github.com/zync/coms
cd coms

# 2. Install dependencies
go mod download

# 3. Generate your server keypair
go run cmd/keygen/main.go

# 4. Configure
cp .env.example .env
# Edit .env with your SERVER_ID, CENTRAL_API_KEY, and the generated keys

# 5. Run
go run cmd/server/main.go
```

Your comS will be available at `http://localhost:3000`.

## Connecting

Clients connect via WebSocket:

```
ws://your-server:3000/connect?token=<scoped_paseto_token>
```

The scoped token is obtained from Zync Central by calling `POST /api/v1/servers/{id}/join`.

## Message Format

All WebSocket messages follow this format:

```json
{
  "t": "zync.channels.message.create",
  "id": "019d...",
  "d": { ... },
  "ts": 1714384200000
}
```

| Field | Description |
|-------|-------------|
| `t`   | Message type — namespaced dot notation |
| `id`  | UUIDv7 — used for deduplication |
| `d`   | Payload — specific to each message type |
| `ts`  | Unix timestamp in milliseconds |

## Namespaces

| Namespace | Owner | Description |
|-----------|-------|-------------|
| `zync.core.*` | Zync | Core protocol (ping, hello, errors, join/leave) |
| `zync.channels.*` | Zync | Text channels, history, search |
| `zync.roles.*` | Zync | Role system *(coming soon)* |
| `com.*` | Community | Custom extensions |

## Configuration

See [docs/configuration.md](docs/configuration.md) for all options.

## Building a Custom comS

You can implement a comS in any language as long as it:

1. Serves `GET /manifest` with the correct response format
2. Accepts WebSocket connections at `GET /connect?token=...`
3. Validates the scoped PASETO token offline using Zync Central's public key
4. Sends a heartbeat to Central every 60 seconds
5. Implements the `zync.core.*` message types

See [docs/extensions.md](docs/extensions.md) to learn how to add custom functionality.

## Log Chain

Every message is appended to a cryptographic log chain:

```
Hash(n) = SHA-256(index || message_id || timestamp || Hash(n-1) || SHA-256(content))
```

This means:
- **Deletion is detectable** — gaps in the index break the chain
- **Tampering is detectable** — changing content invalidates the hash
- **The server's Ed25519 signature** proves Zync signed each entry

Clients can independently verify the chain using the server's public key from the manifest.

See [docs/log-chain.md](docs/log-chain.md) for the full specification.

## Project Structure

```
coms/
├── cmd/server/         Entry point
├── internal/
│   ├── central/        Communication with Zync Central (heartbeat, token verify)
│   ├── channels/       Text channel message handlers
│   ├── config/         Configuration loading
│   ├── logger/         Structured logging
│   ├── logchain/       Cryptographic log chain
│   ├── manifest/       GET /manifest endpoint
│   ├── storage/        SQLite database layer
│   └── ws/             WebSocket hub and client management
└── docs/               Extended documentation
```

## Contributing

PRs are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) first.

## License

MIT — see [LICENSE](LICENSE).
