Write my morning brief from the vault. Read only; do not create or edit any file.

Gather these five things, in this order. Use Grep and Read on the vault; do not guess from memory. Ignore `_templates/`.

1. **Todos due.** Every note in `_todos/` whose frontmatter has `status: open` and a `due:` on or before today. Show the H1 and the due date, and mark the overdue ones.
2. **Up next.** The other open todos in `_todos/` (`status: open`, not already listed), at most 5: those with a `due:` first, nearest date first, then undated ones oldest `created:` first. Show the H1, plus the due date when there is one. If more than 5 remain, end the section with a line saying how many more are open.
3. **Birthdays.** Person notes (`type: person`) with a `birthday:` key in `MM-DD` form matching today, plus those in the next 7 days. Show `name` and the date with its weekday. Today's go first and get a party line; the rest are one line each.
4. **Fixed dates.** Rows of the table under "Fixed dates worth not forgetting" in `dashboard.md` whose date is within the next 30 days. Show the date and the What column, shortened.
5. **Recurring obligations.** Rows of the "Recurring" table in `dashboard.md`. Read the When column as a deadline day of the month (treat "First week" as the 7th, "Early month" as the 10th, "Monthly" and "Through the month" as no deadline). Report a row when its deadline day has passed this month and Last done is not a date in the current month. Show the What column, shortened, and say "not recorded this month" when Last done is `—` or an older date. A missing date means nobody wrote it down, not that it was skipped, so never say "unpaid" or "not done".

Format for WhatsApp. The first line is a one-sentence summary of the whole brief, under 120 characters, plain text with no emoji or formatting, because it may be delivered on its own as a notification headline. Example: `1 overdue todo, Beatriz's birthday Friday, Bruno's baptism Oct 17, ISN not recorded`. Then a blank line, then the sections.

Each section is one header line with a single emoji and the name, then `•` bullets, then a blank line: `✅ Todos due`, `📋 Up next`, `🎂 Birthdays`, `📌 Fixed dates`, `🔁 Recurring`. Add ⚠️ only to overdue todos. Dates as `Mon D`, with the weekday for anything within the next 7 days. No Markdown headers, no bold, no tables, no links, no code. Skip a section entirely when it is empty. Keep the whole message under 22 lines.
