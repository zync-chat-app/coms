# The Zync Log Chain

Every message sent through a Zync comS is appended to a cryptographic log chain. This makes tampering immediately detectable.

## How it works

Each log entry contains:

| Field | Description |
|-------|-------------|
| `index` | Sequential integer — gaps indicate deleted entries |
| `message_id` | UUIDv7 of the message |
| `timestamp` | Unix milliseconds |
| `prev_hash` | SHA-256 of the previous entry — links the chain |
| `content_hash` | SHA-256 of the message content |
| `hash` | SHA-256(index \|\| message_id \|\| timestamp \|\| prev_hash \|\| content_hash) |
| `signature` | Ed25519 signature over `hash` using the server's private key |

## Verification

A client (or auditor) can verify the chain by:

1. Checking that `index` values are sequential with no gaps
2. Recomputing `hash` for each entry and comparing
3. Verifying the `signature` against the server's public key (from the manifest)
4. Checking that `content_hash` matches the actual message content

If any check fails, the chain is broken — meaning the server has tampered with or deleted messages.

```
entry[0]: index=0, prev_hash=0x000...0, hash=H0, sig=S0
entry[1]: index=1, prev_hash=H0,        hash=H1, sig=S1
entry[2]: index=2, prev_hash=H1,        hash=H2, sig=S2
```

If entry[1] is deleted: entry[2].prev_hash = H1, but the chain only has H0 and H2 — the mismatch is immediate.

## Soft deletes

When a message is "deleted" by a user, its content is cleared (`[deleted]`) but the log entry remains. This preserves chain integrity while respecting the user's intent to remove the content.

## Persistence

Chain entries are stored in the `log_chain` table in SQLite alongside messages. On server restart, the chain resumes from the last stored entry.

## Server public key

The server's Ed25519 public key is included in `GET /manifest`. Clients should cache this and use it to verify signatures.
