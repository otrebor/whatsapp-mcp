package main

// Build-tag-free half of the system tray UI (see tray.go and
// docs/plans/tray-ui.md). Everything here is pure logic so the default
// `go test ./...` covers it.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"math"
	"net/url"
	"os"
)

// terminalQR reports whether pairing QR codes are drawn on stdout. The tray
// build turns it off: its stdout is a log file, and a live code must never
// reach disk (see pairingState). The tray shows the code through the
// pairing page instead.
var terminalQR = true

// clientLogLevel and logColor configure the bridge's loggers. The tray build
// uses INFO without ANSI colors: whatsmeow logs the pairing refs and the QR
// payload at DEBUG, which would put a live code in the log file.
var (
	clientLogLevel = "DEBUG"
	logColor       = true
)

type trayIcon int

const (
	trayIconGreen trayIcon = iota
	trayIconAmber
	trayIconGrey
	trayIconRed
)

// trayView is what the tray shows for one bridge state.
type trayView struct {
	Icon    trayIcon
	Status  string // first, disabled menu line and tooltip
	Detail  string // optional second line; empty hides it
	CanPair   bool // enables "Show pairing QR"
	CanLogout bool // enables "Log out…" (logout needs a live connection)
}

// deriveTrayView maps the bridge state to the tray. h is nil until runBridge
// has started the REST server. The first matching rule wins.
func deriveTrayView(h *healthSnapshot) trayView {
	if h == nil {
		return trayView{Icon: trayIconAmber, Status: "Starting…"}
	}
	switch {
	case h.RESTError != "":
		return trayView{Icon: trayIconRed, Status: "API port in use", Detail: h.RESTError}
	case h.PairingState == pairingLoggedOut:
		return trayView{Icon: trayIconGrey, Status: "Logged out: re-pair", CanPair: true}
	case h.PairingState == pairingAwaitingScan || h.PairingState == pairingTimedOut:
		return trayView{Icon: trayIconAmber, Status: "Waiting for QR scan", CanPair: true}
	case h.PairingState == pairingStarting:
		return trayView{Icon: trayIconAmber, Status: "Starting…"}
	case h.Connected && h.OfflineSync.InProgress:
		v := trayView{Icon: trayIconAmber, Status: "Syncing…", CanLogout: true}
		if h.OfflineSync.Messages > 0 {
			v.Detail = fmt.Sprintf("Catching up on %d messages", h.OfflineSync.Messages)
		}
		return v
	case h.Connected:
		return trayView{Icon: trayIconGreen, Status: "Connected", CanLogout: true}
	default:
		return trayView{Icon: trayIconGrey, Status: "Reconnecting…"}
	}
}

// stoppedTrayView is shown when runBridge failed: the tray stays up so the
// user can read the error and open the log.
func stoppedTrayView(err error) trayView {
	return trayView{Icon: trayIconRed, Status: "Bridge stopped", Detail: err.Error()}
}

// pairingPageURL is the browser pairing page with the token in the URL
// fragment. Browsers never send the fragment to the server, and
// web/pairing.js removes it from the address bar.
func pairingPageURL(port int, token string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/pairing#token=%s", port, url.QueryEscape(token))
}

var trayIconColors = map[trayIcon]color.NRGBA{
	trayIconGreen: {R: 0x25, G: 0xD3, B: 0x66, A: 0xFF},
	trayIconAmber: {R: 0xF5, G: 0xA6, B: 0x23, A: 0xFF},
	trayIconGrey:  {R: 0x8E, G: 0x8E, B: 0x93, A: 0xFF},
	trayIconRed:   {R: 0xE5, G: 0x39, B: 0x35, A: 0xFF},
}

const trayIconSize = 32

// Speech-bubble geometry in icon pixels: a round body with a tail pointing
// to the bottom-left corner. The white outline is drawn outside this shape.
var (
	bubbleCenter = [2]float64{17, 15}
	bubbleTail   = [3][2]float64{{4.5, 28}, {6.5, 19}, {13.5, 25.5}}
)

const (
	bubbleRadius = 11.5
	bubbleBorder = 2.0 // white outline width
	bubbleSS     = 4   // supersamples per axis, for anti-aliasing
)

// bubbleSDF is the signed distance from (x, y) to the bubble: negative
// inside, positive outside. Taking the min of the parts is exact outside
// the shape, which is where the outline is drawn.
func bubbleSDF(x, y float64) float64 {
	circle := math.Hypot(x-bubbleCenter[0], y-bubbleCenter[1]) - bubbleRadius
	return math.Min(circle, triangleSDF(x, y, bubbleTail))
}

// triangleSDF is the signed distance from (x, y) to triangle t.
func triangleSDF(x, y float64, t [3][2]float64) float64 {
	d := math.Inf(1)
	inside := true
	var sign float64
	for i := range t {
		ax, ay := t[i][0], t[i][1]
		bx, by := t[(i+1)%3][0], t[(i+1)%3][1]
		ex, ey := bx-ax, by-ay
		px, py := x-ax, y-ay
		h := math.Max(0, math.Min(1, (px*ex+py*ey)/(ex*ex+ey*ey)))
		d = math.Min(d, math.Hypot(px-ex*h, py-ey*h))
		// Inside a triangle means the same side of every edge.
		cross := ex*py - ey*px
		if i == 0 {
			sign = cross
		} else if cross*sign < 0 {
			inside = false
		}
	}
	if inside {
		return -d
	}
	return d
}

// trayIconPNG draws a speech bubble in the state's color with a white
// outline, so it stays visible on both light and dark task bars.
func trayIconPNG(kind trayIcon) []byte {
	fill := trayIconColors[kind]
	img := image.NewNRGBA(image.Rect(0, 0, trayIconSize, trayIconSize))
	const n = bubbleSS * bubbleSS
	for y := 0; y < trayIconSize; y++ {
		for x := 0; x < trayIconSize; x++ {
			var shape, body int
			for sy := 0; sy < bubbleSS; sy++ {
				for sx := 0; sx < bubbleSS; sx++ {
					d := bubbleSDF(
						float64(x)+(float64(sx)+0.5)/bubbleSS,
						float64(y)+(float64(sy)+0.5)/bubbleSS,
					)
					if d <= bubbleBorder {
						shape++
					}
					if d <= 0 {
						body++
					}
				}
			}
			if shape == 0 {
				continue
			}
			// Mix white (outline) with the fill by the share of body samples.
			t := float64(body) / float64(shape)
			mix := func(c uint8) uint8 { return uint8(math.Round(255 + (float64(c)-255)*t)) }
			img.SetNRGBA(x, y, color.NRGBA{
				R: mix(fill.R), G: mix(fill.G), B: mix(fill.B),
				A: uint8(math.Round(255 * float64(shape) / n)),
			})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img) // cannot fail for an in-memory NRGBA image
	return buf.Bytes()
}

// pngToICO wraps one PNG in an ICO container. Windows' tray needs ICO, and
// PNG-compressed entries have been valid since Vista.
func pngToICO(pngData []byte, size int) []byte {
	var buf bytes.Buffer
	// ICONDIR: reserved, type (1 = icon), image count.
	_ = binary.Write(&buf, binary.LittleEndian, [3]uint16{0, 1, 1})
	dim := byte(size)
	if size >= 256 {
		dim = 0 // 0 means 256
	}
	// ICONDIRENTRY: width, height, palette size, reserved, planes, bpp, data size, data offset.
	buf.Write([]byte{dim, dim, 0, 0})
	_ = binary.Write(&buf, binary.LittleEndian, [2]uint16{1, 32})
	_ = binary.Write(&buf, binary.LittleEndian, [2]uint32{uint32(len(pngData)), 6 + 16})
	buf.Write(pngData)
	return buf.Bytes()
}

// rotateLog renames path to path+".1" (replacing an older copy) when it is
// larger than maxBytes. A missing file is not an error.
func rotateLog(path string, maxBytes int64) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() <= maxBytes {
		return nil
	}
	old := path + ".1"
	// Windows cannot rename onto an existing file.
	if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(path, old)
}

// openCommand returns the program and arguments that open target (a URL,
// file or folder) with the OS default handler. Target is always its own
// argument, never part of a shell string.
func openCommand(goos, target string) (string, []string) {
	switch goos {
	case "windows":
		// Unlike `cmd /c start`, keeps "#" and "&" in URLs intact.
		return "rundll32", []string{"url.dll,FileProtocolHandler", target}
	case "darwin":
		return "open", []string{target}
	default:
		return "xdg-open", []string{target}
	}
}

// clipboardCommand returns a program that copies its stdin to the clipboard.
func clipboardCommand(goos string, wayland bool) (string, []string) {
	switch {
	case goos == "windows":
		return "clip", nil
	case goos == "darwin":
		return "pbcopy", nil
	case wayland:
		return "wl-copy", nil
	default:
		return "xclip", []string{"-selection", "clipboard"}
	}
}
