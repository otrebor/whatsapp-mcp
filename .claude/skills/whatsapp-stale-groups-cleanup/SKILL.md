---
name: whatsapp-stale-groups-cleanup
description: Clean up stale WhatsApp groups. Checks the bridge is logged in, scans every group for its last real message, builds a list of groups that are likely no longer needed (inactive, no stored messages, past one-off events), then goes through it oldest first asking for each group whether to leave, leave + delete, skip, or stop. Remembers earlier decisions. Use when asked to clean up / tidy / prune / leave old or unused WhatsApp groups.
---

# Clean up stale groups

Run with the WhatsApp MCP tools (`bridge_status`, `get_pairing_qr`,
`leave_group`, `delete_chat`) and the bundled `scan.py`. Leaving is visible
to every member and deleting can't be undone: **nothing is left or deleted
without the user's answer for that specific group.**

Work folder: `cleanup/` in the repo root (gitignored). Each day's run writes
`cleanup/<YYYY-MM-DD>.csv` (decisions) and
`cleanup/<YYYY-MM-DD>-candidates.json` (the list).

## 0. Preflight

Same as the `bday-gift-group-setup` skill's step 0 (MCP tools available,
`bridge_status` connected, pair if needed). Stop on any failure: leaving and
deleting need a connected bridge.

## 1. Pick the threshold

Ask once for the inactivity threshold: 60, 90, **180 (default)** or 365
days.

## 2. Scan

From the repo root:

```bash
python .claude/skills/whatsapp-stale-groups-cleanup/scan.py scan --days <N>
```

It reads `messages.db` and `whatsapp.db` read-only (honours
`WHATSAPP_DB_PATH` / `WHATSMEOW_DB_PATH`) and prints one line per
candidate: `index | name | jid | last activity | reasons | sender: text`.
Don't page through `list_chats` instead: it returns every direct chat too
and is far too large.

What the scan does, so you can explain it:

- **Activity = the latest stored message.** `chats.last_message_time` also
  moves on history sync and read markers, so it is never used to date a
  group.
- **Reasons**: `inactive` (last message older than the threshold),
  `no messages` (history never synced to the bridge; the group may still
  be busy on the phone), `past event?` (the name looks like a one-off
  event: party, dinner, bday, gift, wedding, stag, trip, a past year, a
  date like `05.07.25` or `13th`).
- **Excluded**: groups with a message in the last 14 days; groups with no
  stored messages and no event-like name (nothing to go on); and any group
  already in a `cleanup/*.csv` log from an earlier run or earlier today.
  Re-running the same day therefore continues where the last run stopped.
- **Order**: dated groups oldest first, then the no-messages groups.
- **Sender**: `me`, a contact name, or empty when the bridge stored the group
  itself as the sender. Show empty as "someone".

Show the user the summary line (groups, already decided, candidates, count
per reason) and the first ~10 names, then start.

## 3. Ask one by one

Use `AskUserQuestion`, one question per group, up to 4 per prompt, in index
order, header `<index>/<total>`. Each question shows the name, the last
activity date with "N weeks/months ago" (or "no stored messages"), the
sender + text snippet, when the user last wrote there if the scan shows it,
and the reasons. Name obvious false positives (e.g. a future year such as
"present 2027"). Options, in this order:

- **Skip**: do nothing (remembered: not asked again in later runs).
- **Leave**: leave the group, keep the chat history.
- **Leave + delete**: leave, then delete the chat from all devices.
- **Stop**: end the workflow.

Apply the answers of a prompt (step 4) before asking the next. If an answer
is **Stop** and a later answer in the same prompt is Leave or Leave +
delete, don't guess: ask whether to apply those and then stop, stop right
away, or treat the Stops as Skip and continue. Otherwise apply the answers
before the Stop and go to step 5. Groups after a Stop are not logged, so
they come back next run.

## 4. Apply and log

- **Leave**: `leave_group(jid)`. If it fails because we're no longer a
  member, say so: for Leave + delete continue with the delete, otherwise
  log result `already left`.
- **Leave + delete**: `delete_chat(jid)` only after the leave succeeded (or
  we were already out). It works for groups with no stored messages too
  (the bridge falls back to the chat's `last_message_time`). If an older
  bridge answers "No stored messages", log result `delete by hand` and keep
  going.
- Any other error: report it, log it, and keep going.

Log every answered group as soon as it's applied:

```bash
python .claude/skills/whatsapp-stale-groups-cleanup/scan.py log <index> <skip|leave|leave+delete> [result]
```

`result` is `ok`, `already left`, `delete by hand` or the error message.
After each prompt, one line in chat: what was left / deleted / skipped, and
any errors.

## 5. Summary

Give a short recap: counts for left, left + deleted, skipped, failed, and
not reached (when stopped early, name the next group). List any `delete by
hand` chats so the user can remove them on the phone. Point to
`cleanup/<YYYY-MM-DD>.csv`.
