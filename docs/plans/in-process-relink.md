# Plan: log in and out without restarting the bridge

Status: implemented on `feat/in-process-relink` (phases 2–5 in one branch;
see `whatsapp-bridge/session.go`). The phase 1 spike's live checks (a real
logout and re-link) are the manual tests below. Follows up phase 3 of
[`qr-pairing.md`](./qr-pairing.md), which fell back to exiting with code 3
after a logout.

## Problem

When the phone unlinks the bridge (`events.LoggedOut`, for example "401:
logged out from another device"), the bridge exits with `exitCodeLoggedOut`
(3) and something has to start it again:

```
[Client WARN] ⚠️  Device logged out (401: logged out from another device); exiting with code 3 so a restart can show a new QR code
Logged out of WhatsApp; restart the bridge to link again (exit code 3).
```

- Under launchd `KeepAlive` or systemd `Restart=on-failure` this works. Run by
  hand (`go run .`) or from an agent-started window, the bridge just stops,
  `bridge_status` reports `reachable: false`, and `get_pairing_qr` can't help.
- There is no way to log out on purpose (for example to switch accounts)
  other than unlinking from the phone or deleting `store/whatsapp.db`.
- A QR timeout after 3 attempts also ends the process (`return` in the connect
  loop in `main.go`), with the same result.

## Why the process exits today

From `pairing.go` (`exitCodeLoggedOut`) and the phase 3 spike: on
`LoggedOut`, whatsmeow calls `Device.Delete`, which clears `Store.ID` and sets
`Deleted`. The same `*whatsmeow.Client` then fails `Connect` with
`store.ErrDeviceDeleted`, and its app-state processor keeps a reference to the
old store, so assigning a new device to `client.Store` is not safe.

That rules out **reusing** the client. It doesn't rule out **replacing** it:
build a new `*whatsmeow.Client` on a new device from the same
`sqlstore.Container`, in the same process. The old client is disconnected and
dropped, so no stale state is carried over.

## Goal

1. A runtime logout moves the bridge to `logged_out`, then to `awaiting_scan`
   with a new QR code, without the process exiting. `/api/health`,
   `/api/pairing/qr.png` and the read side keep working throughout.
2. An MCP client can log out on purpose (`POST /api/logout`, `logout` tool).
3. A QR timeout leaves the bridge running in `timed_out`; pairing can be
   started again (`POST /api/pairing/start`) without a restart.

Non-goals: several accounts at once, a web UI, wiping `messages.db` on logout.

## Design

### Bridge

**Session manager (new file `session.go`).** One goroutine owns the WhatsApp
client's lifecycle and loops:

```
for {
    device := container.GetFirstDevice(ctx)  // first session: stored device, or NewDevice() if none
                                             // after a logout: container.NewDevice(), see below
    client := whatsmeow.NewClient(device, logger)
    client.AddEventHandler(newEventHandler(client, messageStore, sess, logger))
    sessions.set(client)                  // REST handlers see the new client

    if device.ID == nil { pair(client) }  // QR flow, see below
    else { connect(client) }              // existing retry + reconnect logic

    wait for: loggedOut | logoutRequested | shutdown
    client.Disconnect()
    if shutdown { return }
    wait until the old device's row is gone (bounded, see below)
    // loop with container.NewDevice() → new QR
}
```

- **Logout race.** whatsmeow dispatches `events.LoggedOut` in a goroutine
  *before* it calls `Store.Delete` (`connectionevents.go`, both the
  `device_removed` stream error and the logged-out connect failure). If the
  manager called `GetFirstDevice` right away it could get the dead device's
  row back. Reading the old `Device.Deleted` field from the manager goroutine
  would be a data race, and calling `Delete` concurrently with whatsmeow's own
  call is no better. Instead the manager polls `container.GetAllDevices` (a
  database read) for up to a few seconds until the old JID is gone, then
  starts the next session on `container.NewDevice()`. If the row is still
  there at the timeout, log it. It would otherwise be picked up again at the
  next process start. After `POST /api/logout` there is no wait: `Logout` has
  already deleted the device when it returns.
- **Logout during connect.** The log in the problem statement is the
  connect-time path: `Connect` returns nil, then a 401 connect failure fires
  `LoggedOut` (today this is the "Failed to establish stable connection"
  branch). `connect(client)` must watch the session's logged-out signal too,
  and not retry `Connect` on a deleted device (it would fail with
  `store.ErrDeviceDeleted`).

- `newEventHandler` is today's `client.AddEventHandler(func(evt) {...})` body
  from `main`, moved into a constructor that takes the client and the
  session's channels. Each session gets its own closure, `reconnectChan` and
  `loggedOutChan`, so an event from an old client can't affect the new one.
- The reconnect goroutine (`reconnectChan` handling at the end of `main`)
  becomes per-session and exits when its session ends. Otherwise it would
  call `Connect` on a deleted client.
- `pair(client)` is today's `GetQRChannel` + `Connect` + `renderPairingQRCodes`
  block. It keeps the 5-minute context and up to 3 attempts. After the last
  attempt it sets `timed_out` and **waits** for `pairingStartRequested` or
  shutdown instead of returning from `main`.
- `exitAfterLogout` and `exitCodeLoggedOut` are removed, as is the
  `goto connectionSuccess` flow. `main` sets up stores, token, port and REST,
  starts the manager, and waits for SIGINT/SIGTERM.

**Client holder.** REST handlers currently capture the one `client` passed to
`startRESTServer` → `newRESTMux` → `register*Endpoint`. Replace it with:

```go
type sessionHolder struct {
    mu     sync.RWMutex
    client *whatsmeow.Client
}
func (h *sessionHolder) current() *whatsmeow.Client
func (h *sessionHolder) set(c *whatsmeow.Client)
```

`newRESTMux` and the `register*Endpoint` helpers (`chat_delete.go`,
`group_info.go`, `history_ondemand.go`, `pairing.go`) take the holder and call
`h.current()` **once at the top of each request**, then pass that client down
as today. Helpers such as `sendWhatsAppMessage`, `downloadMedia` and
`resolveRecipientJID` keep their `*whatsmeow.Client` parameter, so most of the
diff is in handler preambles. `requireConnected` keeps working: a client that
is being replaced is disconnected, so the request gets a 503.

The REST server starts before the manager, so `current()` can be nil at
startup. Either create the first client synchronously in `main` before
`startRESTServer` (preferred: `/api/health` dereferences `client.Store`), or
make every handler treat nil as "not connected".

Check package-level state that may hold client-derived data and decide whether
to reset it per session. Today that is mostly caches keyed by message ID (for
example `rememberOriginalTimestamp`), which are harmless to keep.
`history_ondemand.go` has no package-level state.

**Endpoints** (behind the existing bearer token, loopback only):

- `POST /api/logout`: calls `client.Logout(ctx)`, which sends
  `remove-companion-device` to WhatsApp, disconnects, then deletes the device
  locally. `Logout` does **not** emit `events.LoggedOut`, and its
  `Disconnect` is expected so no `Disconnected` event fires either. The
  handler therefore signals `logoutRequested` itself after `Logout` returns.
  Returns `200 {"success": true}`. `409` if not logged in
  (`ErrNotLoggedIn`). `Logout` needs a live connection to send the request, so
  when logged in but offline return `503` and leave the session alone.
- `POST /api/pairing/start`: in `timed_out`, wakes the manager to start a new
  QR flow. Returns `202`. `409` when paired or already in `awaiting_scan`.
- `GET /api/health`: unchanged fields. `pairing_state` now moves
  `paired → logged_out → awaiting_scan` within one process, so `logged_out`
  is only seen briefly.

**Pairing state.** `bridgePairing` stays global, since there is only one
session at a time. The manager resets `code`/`codeSeq` when a session ends so
a stale QR is never served after a logout.

### MCP server

- `_bridge_status_message` (`whatsapp.py`): `logged_out` → "Logged out; the
  bridge is preparing a new QR code". `timed_out` → "QR code expired; call
  get_pairing_qr to start a new one".
- `get_pairing_qr`: in `timed_out`, `POST /api/pairing/start`, then return the
  first new code (poll the PNG endpoint for up to ~10s). This is the behavior
  phase 3 of `qr-pairing.md` originally planned.
- New tool `logout()`: `POST /api/logout`. The description says it unlinks
  this device from the phone and that re-linking needs a QR scan. It does not
  delete local message history.
- Older bridges without these endpoints return 404: report "bridge too old;
  restart it to re-link" instead of a raw error.

### Docs and skill

- README: replace the "Bridge exits with code 3" troubleshooting entry and
  document `POST /api/logout`,
  `POST /api/pairing/start` and the `logout` tool.
- `pairing.go` comment and `qr-pairing.md` phase 3: point to this plan.
- `.claude/skills/bday-present/SKILL.md`: `logged_out` → call
  `get_pairing_qr` instead of restarting the bridge.
- Supervisors can keep `Restart=on-failure`. Exit code 3 is no longer used,
  which should be noted as a behavior change in the PR and changelog (via
  the commit message, not by editing `CHANGELOG.md`).

## Security

Call this out in each bridge PR body (`AGENTS.md` rule 8).

- `POST /api/logout` is destructive: anyone holding the bridge token can
  unlink the device. Same trust boundary as `/api/send` (token + loopback),
  but the MCP tool description should say clearly that it unlinks the device.
- After a logout the bridge serves a new QR code without anyone at the
  terminal. A leaked code lets someone link *their* account to this bridge,
  as already noted in `qr-pairing.md`. Mitigations stay the same: token,
  loopback bind, `no-store`, the code is never logged, short code lifetime.
- If a different phone number is linked, `messages.db` keeps the previous
  account's chats, and the MCP tools will show them together. Log a warning
  when the new `Store.ID.User` differs from the last one seen. `messages.db`
  has no settings table today (only `chats`, `messages`, `calls`), so this
  needs a small new one. Don't delete anything automatically.

## Phases (one PR each, aim for under ~300 LOC)

1. **Spike (no PR, notes added here).** In a scratch build: log out from the
   phone, wait for the old device's row to go, then `NewDevice` → `NewClient`
   → `GetQRChannel` on the same `sqlstore.Container`, and confirm pairing
   works and the old client's goroutines stop after `Disconnect`. Also
   confirm `GetFirstDevice` returns a fresh device after a restart that
   follows an in-process re-link. If `NewClient` on the same container turns
   out to be unsafe, re-open the container per session as a fallback.
   (Already checked in the whatsmeow source: `Logout` does not emit
   `LoggedOut`, and `LoggedOut` is dispatched before `Store.Delete`.)
2. **Bridge: client holder refactor.** `sessionHolder`, handlers call
   `current()`, event handler moved into `newEventHandler`. No behavior
   change; existing tests must pass unchanged apart from constructor calls.
3. **Bridge: session manager.** `session.go`, per-session reconnect loop,
   remove `exitAfterLogout`/`exitCodeLoggedOut`, QR timeout waits instead of
   exiting, `POST /api/pairing/start`. Behavior change: no more exit code 3.
4. **Bridge: `POST /api/logout`.**
5. **MCP: `logout` tool, `get_pairing_qr` restarts pairing, status
   messages.** README and skill updates.

Phases 3 and 4 can be combined if the total stays small.

## Tests

Go (`httptest`, `main_test.go` style):

- `sessionHolder`: `set` then `current` returns the new client, and
  concurrent `set`/`current` calls are race-free (run with `-race`).
- Session manager after a logout waits until the old device is gone from
  `GetAllDevices` before creating the next device, and gives up after its
  timeout with a log line.
- Handlers resolve the client per request: swap the client in the holder and
  confirm the next request uses the new one (for example `/api/health`
  `logged_in` flips).
- Session manager with a fake client interface (`Connect`, `Disconnect`,
  `GetQRChannel`, `IsLoggedIn`): logged-out signal → `logged_out`, new session
  started, `bridgePairing` code cleared; QR timeout ×3 → `timed_out` and the
  manager waits; `pairing/start` → new QR flow; shutdown ends the loop.
  This needs a small interface around the `*whatsmeow.Client` calls the
  manager uses. Handlers keep the concrete type.
- `/api/logout`: 401 without token, 409 when not logged in, 503 when logged in
  but offline, 200 and `logoutRequested` sent when logged in (fake `Logout`).
- `/api/pairing/start`: 409 when paired or awaiting scan, 202 when timed out.

Python (monkeypatched `requests`, like `tests/test_pairing_qr.py`):

- `get_pairing_qr` in `timed_out` POSTs `/pairing/start` and returns the image.
- `logout` maps 200, 409, 404 (old bridge) and unreachable.
- `bridge_status` messages for `logged_out` and `timed_out`.

Manual:

1. Pair, then unlink from the phone. The bridge stays up, `bridge_status`
   shows `awaiting_scan`, `get_pairing_qr` returns a code, scanning it
   reconnects. Sending works.
2. `logout` tool → the device disappears from the phone's Linked Devices → new
   QR code.
3. Leave the QR unscanned for 3 attempts → `timed_out`, process still running,
   `get_pairing_qr` starts a new one.
4. Ctrl+C at each state exits cleanly.

## Open questions

- Should a QR timeout keep retrying on its own with backoff, instead of
  waiting for `/api/pairing/start`? Waiting avoids generating codes nobody
  is looking at. Retrying is friendlier for terminal users.
- Should `logout` take an option to also clear `messages.db`? Proposed: no,
  out of scope. Users can delete the file.
- Roadmap fit: "bridge stability" / pairing. Confirm in the issue before
  phase 2.
