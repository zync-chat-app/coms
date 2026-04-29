# Changelog

All notable changes to the Zync comS reference implementation.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- Initial implementation of the Zync Protocol v1.0
- WebSocket hub with connection management
- Text channel support (`zync.channels.*`)
- Cryptographic log chain with Ed25519 signatures
- SQLite storage with FTS5 full-text search
- Offline PASETO token verification
- Automatic heartbeat to Central
- Server manifest endpoint (`GET /manifest`)
- Typing indicators
- Message edit history
- Soft-delete (content cleared, entry preserved for log chain)
