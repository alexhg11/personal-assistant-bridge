# personal-assistant-bridge

Small Go service that connects a WhatsApp number (Meta Cloud API) to a Claude Code instance running against an Obsidian vault on the same box.

```
WhatsApp → Meta webhook → nginx (TLS) → this bridge → sudo -u assistant claude-run → reply via Graph API
```

The bridge holds the Meta secrets and the git deploy key. Claude Code runs as a different OS user through one sudo-allowed wrapper script and never sees them.

## What it does

- `GET /webhook` — Meta's verification handshake.
- `POST /webhook` — verifies `X-Hub-Signature-256`, keeps messages only from the allow-listed sender, acks with 200 immediately, processes in a single worker.
- Text → prompt. Images → downloaded into the vault's media folder, path given to Claude.
- One Claude session per sender, resumed on every turn. `/clear` starts a new one. `/ping`, `/help`.
- Replies chunked to WhatsApp's size limit.
- After each run: `git add -A && git commit` (git dir outside the vault) and push.
- Duplicate webhook deliveries are dropped by message id (SQLite).

## Build

```bash
go test ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/personal-assistant-bridge .
```

On the box itself: `go build -o /usr/local/bin/personal-assistant-bridge .`

## Deploy

See `deploy/`: systemd unit, env file example, nginx location block, privacy page.
