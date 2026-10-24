# Plan: link WhatsApp via QR code from the MCP client

Status: implemented on `feat/qr-pairing`. Phase 3 took the fallback (see
below). [`in-process-relink.md`](./in-process-relink.md) later replaced it:
the bridge now re-links without exiting and serves `POST /api/pairing/start`.

## Problem

Today the only way to link the bridge to WhatsApp is to watch the terminal it
runs in:

- The QR code is printed as text to stdout at startup, and only when there is no
  stored session (`whatsapp-bridge/main.go`, `renderPairingQRCodes` and the
  connect loop around `client.GetQRChannel`).
- The REST API starts only **after** pairing succeeds (`startRESTServer` is
  called after `connectionSuccess:`). While waiting for a scan the bridge
  looks exactly like a bridge that isn't running, so `bridge_status` reports
  `reachable: false` and the MCP client can't help.
- A runtime logout (`events.LoggedOut`) only logs a warning. Re-linking needs a
  manual restart, and the user has to find the terminal again.

When the bridge runs under launchd, systemd or a hidden window, or is started
by an agent, the user never sees the QR code.

## Goal

An MCP client (and the `bday-present` skill's preflight) can:

1. tell "not running" vs "running, not logged in" vs "logged in, temporarily
   disconnected" vs "connected";
2. show the user the current QR code as an image, refreshed as WhatsApp rotates
   it;
3. know when the scan succeeded.

Non-goals: a web UI, multiple accounts, changing what gets synced at pair time.

## Design

### Bridge

**Pairing state.** Add a small, mutex-guarded `pairingState` (new file
`pairing.go`):

| Field | Meaning |
|---|---|
| `state` | `starting`, `awaiting_scan`, `timed_out`, `logged_out`, `paired` |
| `code` | current QR payload (never logged) |
| `codeSeq` | increments on every rotation |
| `codeIssuedAt` | when the current code arrived |
| `lastError` | last pairing failure, for diagnostics |

`renderPairingQRCodes` already takes a `renderQR` callback. Pass one that both
draws to the terminal (current behavior) and stores the code in
`pairingState`. Success, timeout and `events.LoggedOut` update `state`.

**Start REST before pairing.** Move `resolveMediaRoots()` and
`startRESTServer(...)` above the connect/pair loop. Both are already
independent of the WhatsApp connection (port and token are resolved early for
the same reason). Endpoints that need WhatsApp get a `requireConnected`
wrapper returning `503 {"success": false, "message": "not connected to WhatsApp"}`
when `!client.IsConnected()`, instead of whatever whatsmeow would return. Audit
each `HandleFunc` registration in `newRESTMux` and the `register*Endpoint` helpers.

**Endpoints** (all behind the existing bearer-token `auth` wrapper, loopback-only):

- `GET /api/health`: add `logged_in` (`client.Store.ID != nil`) and
  `pairing_state`. Keep the existing fields and status codes so current callers
  keep working.
- `GET /api/pairing/qr.png`: PNG of the current code, with
  `Cache-Control: no-store` and an `X-Pairing-Seq` header. `404` when there's no
  code (already paired, or not in `awaiting_scan`).
- `POST /api/pairing/start` (phase 3): after a logout, start a new pairing on
  the same client without restarting the process. `409` if already paired.

**PNG rendering.** `rsc.io/qr` is already in `go.mod` as an indirect dependency
of `qrterminal`, and has `qr.Encode(code, qr.L).PNG()`. Promote it to a direct
dependency; no new module.

### MCP server

- `bridge_status`: pass through `logged_in` and `pairing_state`. The message
  becomes specific ("waiting for QR scan", "logged out, re-link needed").
- New tool `get_pairing_qr()`:
  - awaiting scan → returns `Image(data=png, format="png")` (same mechanism as
    `view_media`) plus text: "Scan in WhatsApp > Settings > Linked Devices >
    Link a device. Codes rotate about every 20s; call again if it expired.
    Poll `bridge_status` until `connected: true`."
  - already paired → `{"success": true, "message": "already linked"}`
  - logged out → triggers `POST /api/pairing/start`, then returns the first code
  - bridge unreachable → same error shape as `bridge_status`
- Optional parameter `open_viewer: bool = False`: write the PNG to a private
  temp dir and open it in the OS image viewer, for clients that don't render
  images. Opt-in because it runs a local command. The file is deleted on the
  next call or on success.

### Skill (`.claude/skills/bday-present`)

The preflight becomes: `bridge_status` → not reachable: start the bridge in a
visible window (as today) → `pairing_state == awaiting_scan`: `get_pairing_qr`
and poll until connected (5 min cap, matching the bridge's QR context timeout).

## Security

Call this out in each bridge PR body (`AGENTS.md` rule 8).

- A leaked QR code lets *someone else* link *their* account to this bridge
  (and its stored data paths, webhook, etc.). Keep the endpoints behind the
  existing token, loopback bind and host allow-list; send `no-store`; never log
  the code or write it to disk on the bridge side.
- The QR image passes through the MCP client and may end up in a model
  transcript. Codes expire within about 20–60s, which limits the exposure;
  document this in the tool description.
- `open_viewer` executes a local program. Use a fixed `os.startfile` /
  `open` / `xdg-open` call with a file path we created, never user input.
- Starting REST earlier widens the window in which the API is reachable. The
  `requireConnected` guard ensures nothing touches WhatsApp before pairing.

## Phases (one PR each, all under ~300 LOC)

1. **Bridge: pairing state + health fields.** `pairing.go`, callback wiring,
   `logged_in`/`pairing_state` in `/api/health`. No startup reordering yet.
   Low risk, useful on its own.
2. **Bridge: REST before pairing + QR endpoint.** Reorder startup, add
   `requireConnected`, add `/api/pairing/qr.png`. Behavior change: the port
   opens before login, so call it out in the PR and README.
3. **Bridge: re-pair after logout.** Spike first: confirm whatsmeow clears
   `Store.ID` on `LoggedOut` and accepts a new `GetQRChannel` + `Connect` on
   the same `*whatsmeow.Client`. If it doesn't, fall back to exiting with a
   distinct code so a supervisor restarts the bridge, and document that.

   **Outcome:** it doesn't. `Device.Delete` clears `ID` but sets `Deleted`,
   and `Connect` then returns `store.ErrDeviceDeleted`. The client's app-state
   processor also holds the old store, so swapping `client.Store` is unsafe.
   The bridge now records `logged_out` and exits with code 3; launchd
   `KeepAlive` / systemd `Restart=on-failure` or the skill restart it into
   `awaiting_scan`. `get_pairing_qr` reports the logout instead of calling
   `/pairing/start`.
4. **MCP: `bridge_status` fields + `get_pairing_qr`.** Tool, README section,
   tests.
5. **Skill preflight** uses `get_pairing_qr`.

## Tests

Go (`main_test.go` style, `httptest`):

- The `renderQR` callback updates `pairingState.code`/`codeSeq` on every
  rotation (extend `TestRenderPairingQRCodes_RendersEveryCode`).
- `/api/pairing/qr.png`: 401 without a token; 404 with no code; 200 with PNG
  magic bytes and `no-store` when a code is set.
- `/api/health` reports `logged_in`/`pairing_state` for each state.
- `requireConnected` returns 503 for a disconnected client on, for example,
  `/api/send`.

Python (monkeypatched `requests`, like `tests/test_bridge_status.py`):

- `get_pairing_qr` returns an `Image` for a PNG response, "already linked" on
  404 with `logged_in`, an error when unreachable, and calls `/pairing/start`
  when logged out.
- `bridge_status` maps the new fields.

Manual: delete `store/whatsapp.db`, run the bridge hidden, link from the MCP
client only; log out from the phone and re-link without touching the bridge.

## Open questions

- Is opening the REST port before login acceptable for existing deployments?
  Anything that treats "port open" as "ready" should use `/api/health`, which
  already returns 503 when disconnected.
- Should phone-number pairing (`client.PairPhone`, an 8-character code typed on
  the phone) be offered too? It helps when the only screen is the phone itself.
  Proposed as a follow-up, not part of v1.
- Roadmap fit: "Pair-time configuration" (bridge stability) and "Health
  endpoints" (observability). Confirm in the issue.
