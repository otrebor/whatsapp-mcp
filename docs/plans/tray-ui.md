# Plan: optional system tray UI for the bridge

Status: proposed on `feat/tray-ui`. Tracking issue: TBD.

## Problem

On a developer laptop the bridge runs as a console process. To tell whether
it's connected, waiting for a QR scan, syncing, or dead, you have to find its
terminal window or call `/api/health` with the bearer token. Re-pairing means
finding the terminal QR or building the `/pairing#token=…` URL by hand. On
Windows the console window also has to stay open the whole time.

## Goal

An **opt-in** tray / menu bar icon that shows bridge state at a glance and
offers a small menu: re-pair, open the store folder, open the log, quit
cleanly.

- Compiled only with `go build -tags tray`. Default builds, Docker and servers
  are unchanged.
- No changes to the REST API, `messages.db` / `whatsapp.db`, or the MCP server.

Non-goals for v1: logout from the tray (destructive, and the tray has no
confirm dialog), toggling webhooks at runtime, autostart at login, installers
or `.app` bundles.

## What already exists

- **Status.** The `/api/health` handler (`whatsapp-bridge/main.go`,
  `mux.HandleFunc("/api/health", …)`) builds its payload inline from
  `whatsappReady(client)`, `bridgePairing.snapshot()` and
  `bridgeOfflineSync.snapshot()`.
- **Pairing page.** `/pairing` (`pairing_page.go`) takes the token in the URL
  fragment (`#token=…`) and `web/pairing.js` removes it with
  `history.replaceState`. "Show pairing QR" just opens that URL.
- **Shutdown.** `main()` creates `signal.NotifyContext(SIGINT, SIGTERM)`, and
  `manager.run(ctx, first)` returns when that context is cancelled. Tray Quit
  only has to cancel the same context.
- **REST bind errors are only printed** (`startRESTServer`, the
  `ListenAndServe` goroutine). A second instance keeps running with no API.
- **Paths are relative.** `store/` and `store/.bridge-token` resolve against
  the working directory. A tray app started from Explorer, Finder or autostart
  gets an unpredictable working directory.

## Design

### 1. Restructure `main()` (no behavior change)

```go
func main() {
    flag.Parse()
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()
    if err := runUI(ctx, stop, runBridge); err != nil {
        os.Exit(1)
    }
}

// runBridge is the current body of main. Early `return`s become returned errors.
func runBridge(ctx context.Context) error { … }
```

### 2. Shared status snapshot

Move the body of the `/api/health` handler into:

```go
type healthSnapshot struct {
    Connected      bool
    LoggedIn       bool
    PairingState   string
    PairingError   string
    OfflineSync    offlineSyncSnapshot
    HistoryWipedAt *time.Time
    RESTError      string // new; empty unless the REST server failed to bind
}

func bridgeHealth(sessions *sessionHolder, store *MessageStore) healthSnapshot
```

The handler turns the snapshot into the same JSON map it builds today, so the
output is byte-for-byte identical. `RESTError` is not added to the JSON in this
change. The tray calls `bridgeHealth` directly, with no HTTP round trip.

`startRESTServer` stores a bind failure in a package-level `atomic.Value` that
`bridgeHealth` reads. The headless build still prints the error as today.

### 3. Files

| File | Build tag | Contents |
|---|---|---|
| `ui_headless.go` | `!tray` | `runUI` just calls `run(ctx)` |
| `tray.go` | `tray` | `systray.Run` on the main goroutine, bridge in a goroutine, menu wiring, OS open helpers |
| `tray_state.go` | none | Pure functions: `healthSnapshot` → label, icon, tooltip; pairing URL builder; log rotation |
| `tray_state_test.go` | none | Table tests for the above |
| `tray_icons/*.{ico,png}` | — | `go:embed` icons, three states, ICO for Windows and PNG/template for macOS and Linux |

Pure logic stays in untagged files so the default `go test ./...` and
golangci-lint cover it.

### 4. Lifecycle in tray mode

1. `runUI` calls `systray.Run(onReady, onExit)` on the main goroutine. macOS
   requires this.
2. `onReady` sets the initial icon, builds the menu, and starts
   `runBridge(ctx)` in a goroutine. When `runBridge` returns, whether from an
   error or a normal end, it calls `systray.Quit()`. A startup error is logged
   and makes the process exit non-zero.
3. A poller calls `bridgeHealth()` every 2 s and updates the icon, status line,
   tooltip and menu-item enablement only when the derived state changed.
4. Quit → `stop()` → wait for `runBridge` to return (at most about 5 s) →
   `systray.Quit()`.

### 5. Menu

```
● Connected                   (status line, disabled)
  Syncing 1,204 messages…     (visible only during offline sync)
─────────
Show pairing QR               (enabled when pairing_state ∈ awaiting_scan|timed_out|logged_out)
Open store folder
Open log file
Copy bridge token
─────────
Quit
```

How state maps to icon and text (first match wins):

| Condition | Icon | Status text |
|---|---|---|
| `RESTError != ""` | red | API port in use |
| `PairingState == logged_out` | grey | Logged out: re-pair |
| `PairingState ∈ {awaiting_scan, timed_out}` | amber | Waiting for QR scan |
| `PairingState == starting` | amber | Starting… |
| connected and offline sync running | amber | Syncing… |
| connected | green | Connected |
| otherwise (paired, not connected) | grey | Reconnecting… |

Actions:

- **Show pairing QR** opens `http://127.0.0.1:<port>/pairing#token=<token>` in
  the default browser.
- **Open store folder / log file** opens the path with the OS handler
  (`explorer` / `open` / `xdg-open`, using `exec.Command` with no shell).
- **Copy bridge token** writes the token to the clipboard. This could use the
  OS clipboard tool (`clip` / `pbcopy` / `xclip`/`wl-copy`) instead of adding
  a dependency.

### 6. Tray-only startup behavior

- **Working directory.** `os.Chdir(filepath.Dir(os.Executable()))`, unless the
  new flag `--workdir <dir>` is given. This keeps `store/` next to the binary.
  Headless behavior is unchanged.
- **Logging.** Point `os.Stdout`/`os.Stderr` at `store/bridge.log`.
  `waLog.Stdout` and `fmt.Print*` read `os.Stdout` when they're called, so
  reassigning it is enough. Also call `debug.SetCrashOutput(f)` so panics are
  kept. At startup, if the file is over 10 MB, rename it to `bridge.log.1`,
  replacing any older copy.
- **No tray host** (for example GNOME without AppIndicator): log a warning and
  run headless.

### 7. Platform notes

- **Windows:** build with `-ldflags -H=windowsgui` so no console window opens.
  The icon must be ICO.
- **macOS:** cgo is required, which the build already needs for go-sqlite3. Use
  `SetTemplateIcon` so it follows light and dark mode. Check whether a plain
  binary shows a Dock icon; an `.app` bundle with `LSUIElement` is a later
  change.
- **Linux:** DBus StatusNotifierItem. Works on KDE, XFCE and others. GNOME
  needs the AppIndicator extension; document this.

## Dependency

`fyne.io/systray`, the maintained fork of getlantern/systray. On Linux it adds
`godbus/dbus`. It's only compiled with `-tags tray`, but it appears in
`go.mod` / `go.sum`. Justify it in the PR description, as AGENTS.md rule 7
requires.

## Security

- "Show pairing QR" and "Copy bridge token" expose the bearer token to the
  browser launch command line and the clipboard, where other processes of the
  same user can read it. That's the same trust boundary as
  `store/.bridge-token` (mode 0600, owned by the user). The token is only ever
  put in the URL fragment, never in the query string, so it isn't sent to the
  server or logged.
- URLs always target `127.0.0.1:<port>`, never a configurable host.
- `exec.Command` gets fixed program names and the path as a separate argument,
  never a shell string.

## Tests

- `tray_state_test.go`:
  - every row of the state table, including `RESTError`
  - the pairing URL puts the token only in the fragment and escapes it
  - log rotation on a temp directory
- `main_test.go`: the `/api/health` JSON is unchanged after the
  `bridgeHealth()` extraction (connected, disconnected, pairing error, history
  wiped).
- Manual: Windows 11, macOS (arm64), Linux KDE or GNOME with AppIndicator. Check:
  - icon transitions through pair → connected → phone unlinks → re-pair
  - Quit shuts down cleanly with no stray process
  - a second instance shows "API port in use"

## CI

- New job `Go Build (tray)`: `go build -tags tray ./...` on `windows-latest`
  and `macos-latest`. Informational (`continue-on-error: true`) until stable.
- golangci-lint: second run with `--build-tags tray`.

## PR breakdown

1. `refactor: extract runBridge and bridgeHealth`: about 80 LOC, no behavior
   change, plus the `/api/health` regression test.
2. `feat: optional system tray UI (-tags tray)`: about 250 LOC plus embedded
   icons.
   - docs: README "Tray mode" section, AGENTS.md commands, `--workdir`, GNOME
     note
   - security callout in the PR body
3. `ci: tray release binaries`: extend `.github/workflows/release.yml` (today it
   only builds linux/amd64) with `whatsapp-bridge-tray-windows-amd64.exe` and
   `whatsapp-bridge-tray-darwin-arm64`. macOS needs a macOS runner because of
   cgo.

## Open questions

- Should "Copy bridge token" be in v1, or is "Show pairing QR" enough?
- Should Windows tray builds use pure-Go SQLite (`CGO_ENABLED=0`) to avoid a
  MinGW toolchain? `history_ondemand.go` already handles timestamps from
  either driver.
- Should autostart at login be a later PR, or is it out of scope?
