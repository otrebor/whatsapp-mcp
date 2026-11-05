package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveTrayView(t *testing.T) {
	cases := []struct {
		name    string
		h       *healthSnapshot
		icon    trayIcon
		status  string
		canPair bool
		detail  bool
	}{
		{"not started", nil, trayIconAmber, "Starting…", false, false},
		{"port in use beats everything", &healthSnapshot{RESTError: "bind: address already in use", Connected: true, PairingState: pairingPaired}, trayIconRed, "API port in use", false, true},
		{"logged out", &healthSnapshot{PairingState: pairingLoggedOut}, trayIconGrey, "Logged out: re-pair", true, false},
		{"awaiting scan", &healthSnapshot{PairingState: pairingAwaitingScan}, trayIconAmber, "Waiting for QR scan", true, false},
		{"timed out", &healthSnapshot{PairingState: pairingTimedOut}, trayIconAmber, "Waiting for QR scan", true, false},
		{"starting", &healthSnapshot{PairingState: pairingStarting}, trayIconAmber, "Starting…", false, false},
		{"syncing with count", &healthSnapshot{PairingState: pairingPaired, Connected: true, OfflineSync: offlineSyncSnapshot{InProgress: true, Messages: 42}}, trayIconAmber, "Syncing…", false, true},
		{"syncing without count", &healthSnapshot{PairingState: pairingPaired, Connected: true, OfflineSync: offlineSyncSnapshot{InProgress: true}}, trayIconAmber, "Syncing…", false, false},
		{"connected", &healthSnapshot{PairingState: pairingPaired, Connected: true}, trayIconGreen, "Connected", false, false},
		{"paired but offline", &healthSnapshot{PairingState: pairingPaired}, trayIconGrey, "Reconnecting…", false, false},
	}
	for _, c := range cases {
		v := deriveTrayView(c.h)
		if v.Icon != c.icon || v.Status != c.status || v.CanPair != c.canPair || (v.Detail != "") != c.detail {
			t.Errorf("%s: got %+v, want icon=%d status=%q canPair=%v detail=%v", c.name, v, c.icon, c.status, c.canPair, c.detail)
		}
	}
	if v := deriveTrayView(&healthSnapshot{PairingState: pairingPaired, Connected: true, OfflineSync: offlineSyncSnapshot{InProgress: true, Messages: 42}}); !strings.Contains(v.Detail, "42") {
		t.Errorf("sync detail = %q, want the message count", v.Detail)
	}
}

func TestStoppedTrayView(t *testing.T) {
	v := stoppedTrayView(errors.New("failed to connect to database: locked"))
	if v.Icon != trayIconRed || v.CanPair || !strings.Contains(v.Detail, "locked") {
		t.Errorf("got %+v", v)
	}
}

func TestPairingPageURL_TokenOnlyInFragment(t *testing.T) {
	raw := pairingPageURL(8123, "ab+c/d&e=f")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "127.0.0.1:8123" || u.Path != "/pairing" || u.RawQuery != "" {
		t.Errorf("url = %s", raw)
	}
	frag, err := url.ParseQuery(u.EscapedFragment())
	if err != nil {
		t.Fatal(err)
	}
	if frag.Get("token") != "ab+c/d&e=f" {
		t.Errorf("token round-trip = %q", frag.Get("token"))
	}
}

func TestTrayIconPNG_Decodes(t *testing.T) {
	for kind := range trayIconColors {
		img, err := png.Decode(bytes.NewReader(trayIconPNG(kind)))
		if err != nil {
			t.Fatalf("icon %d: %v", kind, err)
		}
		b := img.Bounds()
		if b.Dx() != trayIconSize || b.Dy() != trayIconSize {
			t.Errorf("icon %d: size %v", kind, b)
		}
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Errorf("icon %d: corner not transparent", kind)
		}
		cr, cg, cb, _ := img.At(trayIconSize/2, trayIconSize/2).RGBA()
		want := trayIconColors[kind]
		if uint8(cr>>8) != want.R || uint8(cg>>8) != want.G || uint8(cb>>8) != want.B {
			t.Errorf("icon %d: center color wrong", kind)
		}
	}
}

func TestPNGToICO_Header(t *testing.T) {
	p := trayIconPNG(trayIconGreen)
	ico := pngToICO(p, 32)
	var dir [3]uint16
	if err := binary.Read(bytes.NewReader(ico[:6]), binary.LittleEndian, &dir); err != nil {
		t.Fatal(err)
	}
	if dir != [3]uint16{0, 1, 1} {
		t.Errorf("ICONDIR = %v", dir)
	}
	if ico[6] != 32 || ico[7] != 32 {
		t.Errorf("entry size = %dx%d", ico[6], ico[7])
	}
	size := binary.LittleEndian.Uint32(ico[14:18])
	offset := binary.LittleEndian.Uint32(ico[18:22])
	if int(size) != len(p) || offset != 22 || !bytes.Equal(ico[22:], p) {
		t.Errorf("size=%d offset=%d len(png)=%d", size, offset, len(p))
	}
}

func TestRotateLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bridge.log")

	if err := rotateLog(path, 10); err != nil {
		t.Fatalf("missing file: %v", err)
	}

	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(path, "small")
	if err := rotateLog(path, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Error("small file was rotated")
	}

	write(path+".1", "older")
	write(path, "this is over ten bytes")
	if err := rotateLog(path, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("log still present after rotation")
	}
	if got, _ := os.ReadFile(path + ".1"); string(got) != "this is over ten bytes" {
		t.Errorf("rotated content = %q", got)
	}
}

func TestOpenAndClipboardCommands(t *testing.T) {
	target := "http://127.0.0.1:8080/pairing#token=a&b"
	for _, goos := range []string{"windows", "darwin", "linux"} {
		name, args := openCommand(goos, target)
		if name == "" || len(args) == 0 || args[len(args)-1] != target {
			t.Errorf("%s: openCommand = %s %v, want target as its own last argument", goos, name, args)
		}
	}
	if name, _ := clipboardCommand("linux", true); name != "wl-copy" {
		t.Errorf("wayland clipboard = %s", name)
	}
	if name, args := clipboardCommand("linux", false); name != "xclip" || len(args) != 2 {
		t.Errorf("x11 clipboard = %s %v", name, args)
	}
}
