Write my morning brief from the vault. Read only; do not create or edit any file.

Gather these four things, in this order. Use Grep and Read on the vault; do not guess from memory.

1. **Todos due.** Every note in `_todos/` whose frontmatter has `status: open` and a `due:` on or before today. Show the H1 and the due date. Mark the overdue ones. Ignore `_templates/`.
2. **Birthdays.** Person notes (`type: person`) with a `birthday:` key in `MM-DD` form matching today, plus those in the next 7 days. Show `name` and `birthday_display`. Today's go first and get a party line; the rest are one line each.
3. **Fixed dates.** Rows of the table under "Fixed dates worth not forgetting" in `dashboard.md` whose date is within the next 30 days. Show the date and the What column, shortened.
4. **Recurring obligations.** Rows of the "Recurring" table in `dashboard.md`. Read the When column as a deadline day of the month (treat "First week" as the 7th, "Early month" as the 10th, "Monthly" and "Through the month" as no deadline). Report a row when its deadline day has passed this month and Last done is not a date in the current month. Show the What column, shortened, and say "not recorded this month" when Last done is `—` or an older date. A missing date means nobody wrote it down, not that it was skipped, so never say "unpaid" or "not done".

Format for WhatsApp: `*bold*` section names, `-` bullets, no Markdown headers, no tables, no links, no code. Skip a section entirely when it is empty. Keep the whole message under 15 lines.

The first line must be a one-sentence summary of the whole brief, under 120 characters, with no formatting, because it may be delivered on its own as a notification headline. Example: `3 todos due, Nashly's birthday Friday, ISN still unpaid`. Then a blank line, then the sections.
