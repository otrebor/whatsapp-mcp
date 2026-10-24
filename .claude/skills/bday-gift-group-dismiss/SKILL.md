---
name: bday-gift-group-dismiss
description: Close a birthday-present WhatsApp group after the party. Asks which "Bday Present" group to dismiss, checks we are admin, posts the thank-you message, removes every other member, then offers to leave the group and delete the chat. Use when asked to dismiss / close / wrap up / self-destruct a birthday present group.
---

# Dismiss a birthday present group

Run with the WhatsApp MCP tools (`list_chats`, `get_group_participants`,
`send_message`, `remove_group_participants`, `leave_group`, `delete_chat`).
Every step after the approval gate is visible to real people and can't be
undone: **nothing is sent or removed before the user approves in step 5.**

Work folder: `birthdays/<name-lowercase>/` in the repo root (gitignored), the
same one `bday-gift-group-setup` used. Message template:
`birthdays/templates/thank-you-message.md` (tracked).

## 0. Preflight

Same as the `bday-gift-group-setup` skill's step 0 (MCP tools available,
`bridge_status` connected, pair if needed). Stop on any failure.

## 1. Pick the group

Ask which group to dismiss. Offer the candidates from
`list_chats(query="Bday Present")` (newest first, with name and JID); if
there are none, ask for the name. Confirm the exact group and JID. If
`birthdays/*/summary.md` mentions that JID, use that folder as the work
folder and read the birthday person's name and event from it.

## 2. Check admin rights

`get_group_participants(group_jid)`; a request error means the bridge is
down, so stop. Find the member with `is_me: true`:

- None marked `is_me` → the bridge is too old for this skill (or we're no
  longer in the group). Stop and say so.
- `is_admin` false → stop: only admins can remove members. Suggest asking an
  admin (list them by name) to make the user admin, or to dismiss it.

Everyone else is the **removal list**: `jid`, `name`, `phone_number`. Write
`dismiss_members.csv` (`name,phone,jid,admin`) into the work folder. If the
group has nobody but us, skip to step 7.

## 3. Collect inputs

Ask only for what's missing (prefill from `summary.md` / the welcome
message in the work folder when present):

- **Birthday person's name**, **event** and **event date**.
- **Total collected** (e.g. `CHF 420`) and **the gift** (e.g. "a weekend in
  Ticino").
- **Gift reaction**: one short line on how it went (e.g. "She loved it! 😍");
  empty is fine.
- **Assistant persona**: default "*Claudio*, Roberto's AI assistant".

## 4. Draft the message

Write `thank-you-message.md` into the work folder from the template, filling
`{ASSISTANT_NAME}`, `{ORGANIZER}`, `{BIRTHDAY_PERSON}`, `{EVENT}`,
`{EVENT_DATE}`, `{TOTAL_AMOUNT}`, `{GIFT}`, `{GIFT_REACTION}`. Drop lines
that don't apply (e.g. the card line if there was no card). WhatsApp
formatting only: `*bold*`, `_italic_`; keep it under ~800 chars. No
`{PLACEHOLDER}` may remain.

## 5. Approval gate

Show the user: group name + JID, the thank-you message, and the removal
list (count + names). Say plainly that removal is immediate, everyone sees
it, and they can only come back through a new invite. Wait for an explicit
go-ahead; apply any edits to the files first.

## 6. Send, then remove

1. `send_message(group_jid, <thank-you message>)`. On failure, stop: don't
   remove anyone without the goodbye.
2. Wait ~10s so the message reaches everyone before they are removed.
3. `remove_group_participants(group_jid, <jids of the removal list>)`. Use
   `jid` (not `phone_number`): it always works, also for LID-only members.
   Send everyone in one call. Never include our own JID (the bridge refuses
   it anyway).
4. `get_group_participants(group_jid)` again: anyone besides us still there
   failed. Record them with the error from `failed`, and report them; don't
   retry blindly.

## 7. Leave and delete (ask first)

Ask: "Leave the group and permanently delete the chat from all your
devices?" Offer: leave + delete (default), leave only, keep both.

- **Leave**: `leave_group(group_jid)`.
- **Delete**: only after a successful leave (the WhatsApp app also requires
  leaving a group before deleting it). `delete_chat(group_jid)`. It's "delete for me": gone
  from the user's phone and `messages.db`, not undoable.

Stop on the first error and report it.

## 8. Summary

Append a `## Dismissed` section to the work folder's `summary.md` (create it
if missing): date, group name + JID, message sent, removed count, anyone not
removed (with error), left / deleted. Give the user a short recap in chat.
