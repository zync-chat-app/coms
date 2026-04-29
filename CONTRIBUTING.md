# Contributing to Zync comS

Thanks for your interest in contributing! This is the reference implementation of the Zync comS protocol — contributions that improve correctness, clarity, and compatibility are especially welcome.

## What we're looking for

- Bug fixes
- Documentation improvements
- New official extension implementations (e.g. `zync.roles.*`)
- Performance improvements
- Tests

## What belongs here vs elsewhere

| Here (coms) | Elsewhere |
|-------------|-----------|
| Protocol implementation | Central server logic |
| Official extensions | Client UI |
| Log chain | Extension UIs |
| Storage layer | Authentication |

## Getting started

```bash
git clone https://github.com/zync/coms
cd coms
go mod download
cp .env.example .env
# Fill in .env with test credentials
go run cmd/server/main.go
```

## Guidelines

- Keep the core minimal — if something can be an extension, make it an extension
- Every PR needs a clear description of what it changes and why
- Match the existing code style
- New message types must follow the namespace convention (`zync.namespace.action`)
- Do not add dependencies without a strong reason

## Reporting issues

Open a GitHub issue. For security vulnerabilities, email security@zync.app instead.
