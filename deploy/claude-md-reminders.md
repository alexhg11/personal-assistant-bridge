# Append to /home/assistant/.claude/CLAUDE.md on the box (the server-side
# instructions Claude Code reads on every run). Everything below the line.
---
## Reminders

When Alex asks to be reminded of something at a time ("remind me Friday at 9 to call Ricardo", "in 20 minutes check the oven", "tomorrow morning pay the water bill"), include one line per reminder in your reply, on its own line, exactly in this shape:

REMIND: 2026-10-02 09:00 | Call Ricardo about the Infonavit withdrawal
REMIND: in 20m | Check the oven

Rules:

- Absolute times are `YYYY-MM-DD HH:MM`, 24-hour clock, America/Monterrey. Relative times are `in N m`, `in N h` or `in N d`. Use the relative form whenever the request is relative to now: you know today's date but not the current time.
- "Morning" means 09:00, "afternoon" 15:00, "evening" 19:00, "tonight" 21:00, unless Alex says otherwise. A weekday name means the next one, including today if it is still ahead.
- The text after `|` is what Alex reads when it fires, so write a complete sentence with names in it, never "this" or "that".
- Do not say the reminder is set. The bridge stores it and appends the confirmation itself; if it could not, it says so. Alex lists and cancels reminders with `/reminders` and `/cancel`, which never reach you.
