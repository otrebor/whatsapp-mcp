//go:build tray

package main

// System tray / menu bar UI, compiled only with `go build -tags tray`.
// See docs/plans/tray-ui.md. Pure logic lives in tray_state.go.

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
)

var trayWorkdirFlag = flag.String("workdir", "",
	"tray builds only: directory that holds store/ (default: the executable's directory)")

const (
	trayLogPath     = "store/bridge.log"
	trayLogMaxBytes = 10 << 20
	trayPollEvery   = 2 * time.Second
	trayQuitTimeout = 5 * time.Second
)

// runUI runs the bridge under a system tray icon. systray.Run must own the
// main goroutine (macOS requirement), so the bridge runs in a goroutine.
func runUI(ctx context.Context, stop context.CancelFunc, run func(context.Context) error) error {
	if err := prepareTrayProcess(); err != nil {
		return err
	}
	t := &trayApp{ctx: ctx, stop: stop, run: run, done: make(chan struct{})}
	systray.Run(t.onReady, func() {})

	stop()
	select {
	case <-t.done:
	case <-time.After(trayQuitTimeout):
		fmt.Println("Bridge did not stop in time; exiting anyway")
	}
	return t.result()
}

// prepareTrayProcess makes a tray launch (Explorer, Finder, login item) behave
// like a terminal launch from the bridge directory, and sends output to a
// log file because there may be no console.
func prepareTrayProcess() error {
	dir := *trayWorkdirFlag
	if dir == "" {
		dir = executableDir()
	}
	if dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("failed to use %s as the bridge directory: %w", dir, err)
		}
	}
	if err := ensureOwnerOnlyDirectory("store"); err != nil {
		return fmt.Errorf("failed to create store directory: %w", err)
	}
	if err := rotateLog(trayLogPath, trayLogMaxBytes); err != nil {
		fmt.Printf("Could not rotate %s: %v\n", trayLogPath, err)
	}
	f, err := os.OpenFile(trayLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	// waLog and fmt.Print* read os.Stdout at call time, so swapping it is
	// enough. systray logs through the standard logger.
	os.Stdout, os.Stderr = f, f
	log.SetOutput(f)
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	terminalQR = false
	clientLogLevel, logColor = "INFO", false
	fmt.Printf("\n=== %s tray start (pid %d) ===\n", time.Now().Format(time.RFC3339), os.Getpid())
	return nil
}

// executableDir is the directory of the running binary, or "" under `go run`
// (whose binary lives in a temporary build directory).
func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if strings.Contains(exe, "go-build") {
		return ""
	}
	return filepath.Dir(exe)
}

type trayApp struct {
	ctx  context.Context
	stop context.CancelFunc
	run  func(context.Context) error
	done chan struct{} // closed when run returns

	mu      sync.Mutex
	err     error
	last    *trayView
	stopped bool // run failed: keep showing the error until Quit

	status, detail, pair, token, logout *systray.MenuItem
}

func (t *trayApp) result() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *trayApp) onReady() {
	systray.SetTooltip("WhatsApp bridge")
	t.status = systray.AddMenuItem("Starting…", "")
	t.status.Disable()
	t.detail = systray.AddMenuItem("", "")
	t.detail.Disable()
	t.detail.Hide()
	systray.AddSeparator()
	t.pair = systray.AddMenuItem("Show pairing QR", "Open the pairing page in your browser")
	store := systray.AddMenuItem("Open store folder", "Open the folder with the databases and media")
	logItem := systray.AddMenuItem("Open log file", "Open the bridge log")
	t.token = systray.AddMenuItem("Copy bridge token", "Copy the REST API bearer token to the clipboard")
	systray.AddSeparator()
	// The tray has no confirm dialog, so the submenu is the confirmation:
	// one stray click cannot unlink the device.
	t.logout = systray.AddMenuItem("Log out…", "Unlink this device from your phone")
	confirmLogout := t.logout.AddSubMenuItem("Yes, unlink this device", "Unlinks the bridge and shows a new QR code. Stored messages are kept unless a different account links.")
	quit := systray.AddMenuItem("Quit", "Disconnect and exit")

	t.apply(deriveTrayView(nil), false)

	go t.runBridge()
	go t.poll()
	go func() {
		for {
			select {
			case <-t.pair.ClickedCh:
				if rt := currentRuntime.Load(); rt != nil {
					t.open(pairingPageURL(rt.port, rt.token))
				}
			case <-store.ClickedCh:
				t.open(absPath("store"))
			case <-logItem.ClickedCh:
				t.open(absPath(trayLogPath))
			case <-t.token.ClickedCh:
				if rt := currentRuntime.Load(); rt != nil {
					t.copyToClipboard(rt.token)
				}
			case <-confirmLogout.ClickedCh:
				t.logoutDevice()
			case <-quit.ClickedCh:
				t.quit()
				return
			}
		}
	}()
}

func (t *trayApp) runBridge() {
	err := t.run(t.ctx)
	t.mu.Lock()
	t.err = err
	t.mu.Unlock()
	close(t.done)
	if err != nil && t.ctx.Err() == nil {
		fmt.Printf("Bridge stopped: %v\n", err)
		t.apply(stoppedTrayView(err), true)
		return
	}
	systray.Quit()
}

func (t *trayApp) poll() {
	tick := time.NewTicker(trayPollEvery)
	defer tick.Stop()
	for {
		var h *healthSnapshot
		if rt := currentRuntime.Load(); rt != nil {
			snap := bridgeHealth(rt.sessions, rt.messageStore)
			h = &snap
		}
		t.apply(deriveTrayView(h), false)
		select {
		case <-t.done:
			return
		case <-tick.C:
		}
	}
}

// apply updates the tray when the view changed. Once final is set (the
// bridge failed) later polls are ignored.
func (t *trayApp) apply(v trayView, final bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || (t.last != nil && *t.last == v) {
		return
	}
	t.stopped = final
	t.last = &v

	systray.SetIcon(trayIconBytes(v.Icon))
	systray.SetTooltip("WhatsApp bridge: " + v.Status)
	t.status.SetTitle(v.Status)
	if v.Detail != "" {
		t.detail.SetTitle(v.Detail)
		t.detail.Show()
	} else {
		t.detail.Hide()
	}
	if v.CanPair && !final {
		t.pair.Enable()
	} else {
		t.pair.Disable()
	}
	if currentRuntime.Load() != nil && !final {
		t.token.Enable()
	} else {
		t.token.Disable()
	}
	if v.CanLogout && !final {
		t.logout.Enable()
	} else {
		t.logout.Disable()
	}
}

// logoutDevice runs the same logout as POST /api/logout. The next poll shows
// the result ("Logged out: re-pair", then "Waiting for QR scan").
func (t *trayApp) logoutDevice() {
	rt := currentRuntime.Load()
	if rt == nil {
		return
	}
	t.logout.Disable()
	go func() {
		fmt.Println("Tray: logging out")
		if err := logoutSession(t.ctx, rt.sessions, "logged out from the tray"); err != nil {
			fmt.Printf("Tray: logout failed: %v\n", err)
			// Force the next poll to redraw, re-enabling the item.
			t.mu.Lock()
			t.last = nil
			t.mu.Unlock()
		}
	}()
}

func (t *trayApp) quit() {
	t.stop()
	go func() {
		select {
		case <-t.done:
		case <-time.After(trayQuitTimeout):
		}
		systray.Quit()
	}()
}

func (t *trayApp) open(target string) {
	name, args := openCommand(runtime.GOOS, target)
	cmd := exec.Command(name, args...)
	hideChildConsole(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Printf("Tray: could not open %s: %v\n", target, err)
		return
	}
	go func() { _ = cmd.Wait() }()
}

func (t *trayApp) copyToClipboard(text string) {
	name, args := clipboardCommand(runtime.GOOS, os.Getenv("WAYLAND_DISPLAY") != "")
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	hideChildConsole(cmd)
	go func() {
		if err := cmd.Run(); err != nil {
			fmt.Printf("Tray: could not copy to the clipboard with %s: %v\n", name, err)
		}
	}()
}

// trayIconBytes returns the icon in the format the current OS's tray expects.
func trayIconBytes(kind trayIcon) []byte {
	p := trayIconPNG(kind)
	if runtime.GOOS == "windows" {
		return pngToICO(p, trayIconSize)
	}
	return p
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
