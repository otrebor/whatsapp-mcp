---
name: bday-present
description: Organize a group birthday present from a WhatsApp birthday-invite group. Creates a "present" group with everyone except the birthday person, records who couldn't be added (privacy settings), DMs them the invite link, and posts the welcome message. Use when asked to set up / organize a birthday present or gift collection for someone.
---

# Birthday present group

Run with the WhatsApp MCP tools (`list_chats`, `get_group_participants`,
`create_group`, `set_group_description`, `get_group_invite_link`,
`send_message`). Phone numbers go to these tools with country code and **no +**
(e.g. `41791234567`).
Every send is visible to real people: **nothing is created or sent before the
user approves the plan in step 5.**

Work folder: `birthdays/<name-lowercase>/` in the repo root (gitignored).
Message templates live in `birthdays/templates/` (tracked).

## 0. Preflight

Reads come from SQLite, so they work even when the bridge is down; only
bridge calls prove it's up. Stop on any failure below, before asking for inputs.

- **MCP tools**: if the `whatsapp` tools aren't available, tell the user to
  start/connect the WhatsApp MCP server.
- **Bridge**: if `bridge_status` exists, call it:
  - `connected: true` → continue.
  - `reachable: false` → start it in a visible window
    (`cd whatsapp-bridge && go run .`), then call `bridge_status` again.
  - `pairing_state: awaiting_scan` → call `get_pairing_qr` (pass
    `open_viewer: true` if you can't show images), ask the user to scan it,
    and poll `bridge_status` every ~10s until `connected: true`. Call
    `get_pairing_qr` again when a code expires. Stop after 5 minutes (the
    bridge's QR timeout).
  - `logged_out` or `timed_out` → call `get_pairing_qr`; the bridge offers
    a new code without a restart. Pair as above. If the bridge stopped
    (`reachable: false`, an older bridge), start it again first.
  - `logged_in: true` but not connected → wait ~30s for the reconnect and
    check again once; still down → stop and say so.
  - Anything else → stop and say the bridge isn't connected.
  Without that tool, the source-group `get_group_participants` call in step 1
  is the bridge check.
- **Freshness**: `list_chats(limit=1)`; if the newest message is hours old,
  warn that the bridge may not be syncing and ask whether to continue.

## 1. Collect inputs

Ask only for what's missing:

- **Source group**: the birthday-invite group (find it with `list_chats` / a name search; confirm the JID).
  Right after confirming it, call `get_group_participants(source_jid)` (reused
  in step 2); a request error means the bridge is down, so stop.
- **Birthday person's name** (as used in messages) and **year** (default: current year).
- **Event**: short label and date (e.g. "apero on *23 Oct*").
- **Contribution**: how to pay (e.g. TWINT to the user's number) and deadline.
- **Assistant persona**: default "*Claudio*, Roberto's AI assistant".

## 2. Read the source group

`get_group_participants(source_jid)` (members have `jid`, `phone_number`,
`lid`, `name`, `is_admin`, `is_super_admin`). Identify the birthday person: by
default the admin(s) that aren't the user. If there are zero or several candidates, or the
name doesn't match the admin, **ask**. Never guess who to exclude.

## 3. Build the guest list

Everyone in the source group except the birthday person and the user (the
user is added automatically as creator). Use `phone_number` when present,
otherwise the member's `jid`. Write `participants.csv`:

```
name,phone,jid,admin
```

## 4. Draft messages

Write both files into the work folder from `birthdays/templates/*.md`, filling
the `{PLACEHOLDERS}` from step 1 (`{ASSISTANT_NAME}`, `{ORGANIZER}`,
`{BIRTHDAY_PERSON}`, `{EVENT}`, `{EVENT_DATE}`, `{PAYMENT_METHOD}`,
`{PAYMENT_CONTACT}`, `{DEADLINE}`); leave `{INVITE_LINK}` for step 8:

- `welcome-message.md`: for the group description and first group message.
  Includes: who's writing (persona), the occasion + event date, 🤫 surprise
  reminder ("please don't mention it to <name>"), how to chip in + deadline,
  gift ideas welcome, the user reads the group too, it's fine to leave.
- `private-invite-message.md`: same content, but explains their privacy
  settings didn't allow adding them directly, and has the line
  `👉 *Join here:* {INVITE_LINK}` (placeholder filled in at step 8).

WhatsApp formatting only: `*bold*`, `_italic_`. Keep each under ~800 chars.

Group name: `Bday Present for <Name> <year>` (`create_group` allows up to 100
characters; if WhatsApp still rejects it, e.g. 406, retry with
`Bday Present <Name> <year>` and tell the user).

## 5. Approval gate

Show the user: group name, guest count + list (name/phone), who is excluded and
why, and both messages. Wait for an explicit go-ahead. Apply any edits to the
files before continuing.

## 6. Create the group

`create_group(name, participants)`. Save the returned `jid`. Then
`set_group_description(jid, <welcome message>)`; if that fails, report it and
continue (the group already exists).

## 7. Record who wasn't added

`create_group` returns the new group's `participants`, and lists refused
members in `message` as `<phone> (error <code>)`. Don't rely on the message
alone: anyone in `participants.csv` who isn't among the new group's members
(match on phone or LID; re-check with `get_group_participants` if unsure) was
not added. Take the error code from `message` when present. Write
`not_added.csv`:

```
name,phone,error_code,reason
```

Reasons: `403` → "privacy settings: can only be invited (send them the invite
link)", `408` → "recently left: can only rejoin via link", otherwise
"WhatsApp error <code>" (or "not added, no code" if none was reported).

## 8. DM the invite link

`get_group_invite_link(group_jid)`. For each row in `not_added.csv`,
`send_message(<phone or jid>, private message with {INVITE_LINK} filled in)`.
Send one at a time and stop on the first send error (report it; don't retry
blindly). Record each result.

## 9. Welcome message in the group

`send_message(group_jid, <welcome message>)`.

## 10. Summary

Write `summary.md` (date, group name + JID, invite link, added count,
not-added list with DM status, any errors) and give the user a short recap
in chat.
