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
- One Claude session per sender, resumed on every turn. `/clear` starts a new one. `/ping`, `/help`, `/reminders`, `/cancel <n>`.
- Replies chunked to WhatsApp's size limit.
- After each run: commit on top of `origin/master` and push (git dir outside the vault). Only paths whose content changed in the work tree since the box's last successful commit are staged, tracked in a second index file (`box.index` in the git dir); a note that is merely behind `origin/master` because Obsidian Sync has not delivered a Mac commit yet is left alone instead of being committed over it.
- Duplicate webhook deliveries are dropped by message id (SQLite).
- `POST /jobs/<name>` — loopback only, not proxied by nginx. Runs the prompt file `<name>.md` from `JOBS_DIR` in a throwaway Claude session and sends the result to the allow-listed number. A reply of exactly `NOTHING` is dropped. Systemd timers in `deploy/` fire it on a schedule.

## Ad-hoc reminders

"Remind me Friday at 9 to call Ricardo" works because Claude answers with a `REMIND: 2026-10-02 09:00 | Call Ricardo` line (or `REMIND: in 20m | …`), following the contract in `deploy/claude-md-reminders.md`, which is appended to the server-side `CLAUDE.md`. The bridge strips those lines, stores the reminders in SQLite, appends its own `⏰ Reminder #n set for …` confirmation, and a scheduler fires them every 30 s through the same delivery path as jobs (template fallback included). `/reminders` lists pending ones, `/cancel <n>` removes one. Times are interpreted in `JOB_TZ`; the `claude-run` wrapper should export `TZ=America/Monterrey` so Claude's idea of today matches.

## Proactive messages and the 24-hour window

Meta only accepts free-form text within 24 hours of the recipient's last message. When a job result is refused with error `131047`, the bridge sends the approved utility template named by `WA_TEMPLATE` with the reply's first line as its one parameter, and parks the full text in SQLite. The next inbound message from the recipient flushes parked texts before Claude sees it.

Create the template once in WhatsApp Manager → Message templates, category **Utility**, one body parameter, for example:

> Your scheduled report is ready: {{1}}
> Reply to this message to receive the full report.

Meta's classifier flags friendlier wording as Marketing; "scheduled report" passes as Utility.

Job prompts put a one-line summary first so that headline reads well on its own.

Until it is approved, leave `WA_TEMPLATE` empty; results outside the window are then logged and parked but nothing is sent.

## Build

```bash
go test ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/personal-assistant-bridge .
```

On the box itself: `go build -o /usr/local/bin/personal-assistant-bridge .`

## Deploy

See `deploy/`: systemd unit, env file example, nginx location block, privacy page, and for scheduled jobs the `assistant-job@.service` template unit, the `assistant-*.timer` files and the prompt files under `deploy/jobs/`.

```bash
sudo install -d -m 755 /etc/personal-assistant/jobs
sudo install -m 644 deploy/jobs/*.md /etc/personal-assistant/jobs/
sudo install -m 644 deploy/assistant-job@.service deploy/assistant-*.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now assistant-morning-brief.timer assistant-evening-lookahead.timer
sudo systemctl start assistant-job@morning-brief   # run one by hand
```
