package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"rsc.io/qr"
)

// Pairing states reported by /api/health as pairing_state.
const (
	pairingStarting     = "starting"      // connect/pair loop not reached yet
	pairingAwaitingScan = "awaiting_scan" // a QR code is live and can be scanned
	pairingTimedOut     = "timed_out"     // the last QR channel ran out of codes
	pairingLoggedOut    = "logged_out"    // the phone unlinked this device
	pairingPaired       = "paired"        // a session is stored
)

// exitCodeLoggedOut is the process exit code after a runtime logout.
//
// whatsmeow deletes the device on events.LoggedOut and marks the store
// Deleted, so the same *whatsmeow.Client refuses to Connect again
// (store.ErrDeviceDeleted), and its app-state processor keeps a reference to
// the old store, so swapping in a fresh device is not safe either. Exiting
// with a distinct code lets a supervisor (launchd KeepAlive, systemd
// Restart=on-failure) or the user start a clean process that shows a new QR
// code.
const exitCodeLoggedOut = 3

// pairingState tracks the QR pairing flow so REST callers can show the code
// instead of relying on the terminal. The code itself is never logged or
// written to disk: anyone holding a live code can link their account to this
// bridge.
type pairingState struct {
	mu           sync.Mutex
	state        string
	code         string
	codeSeq      int
	codeIssuedAt time.Time
	lastError    string
}

type pairingSnapshot struct {
	State        string
	Code         string
	CodeSeq      int
	CodeIssuedAt time.Time
	LastError    string
}

// bridgePairing is shared by main's pairing loop and the REST handlers.
var bridgePairing = &pairingState{state: pairingStarting}

// setCode records a freshly offered QR code and marks the bridge as waiting
// for a scan.
func (p *pairingState) setCode(code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = pairingAwaitingScan
	p.code = code
	p.codeSeq++
	p.codeIssuedAt = time.Now()
}

// setState moves to a non-scanning state and drops any live code. errMsg is
// kept for diagnostics when non-empty.
func (p *pairingState) setState(state, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = state
	p.code = ""
	if errMsg != "" {
		p.lastError = errMsg
	}
}

// setError records a failure without changing the state, e.g. a connect
// attempt that will be retried.
func (p *pairingState) setError(errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastError = errMsg
}

func (p *pairingState) snapshot() pairingSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return pairingSnapshot{
		State:        p.state,
		Code:         p.code,
		CodeSeq:      p.codeSeq,
		CodeIssuedAt: p.codeIssuedAt,
		LastError:    p.lastError,
	}
}

// whatsappReady reports whether the client can act on WhatsApp: online and
// logged in. IsConnected alone is not enough: while a QR code is waiting to be
// scanned the websocket is already open, but Store.ID is nil and any send
// dereferences it.
func whatsappReady(client *whatsmeow.Client) bool {
	return client.IsConnected() && client.IsLoggedIn()
}

// requireConnected writes a 503 and returns false unless whatsappReady. Since
// the REST server starts before pairing, every handler that talks to WhatsApp
// must call this (or check whatsappReady) after validating its input.
func requireConnected(w http.ResponseWriter, client *whatsmeow.Client) bool {
	if whatsappReady(client) {
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": false,
		"message": "not connected to WhatsApp",
	})
	return false
}

// registerPairingEndpoints serves the live pairing QR code as a PNG.
func registerPairingEndpoints(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/pairing/qr.png", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")

		snap := bridgePairing.snapshot()
		if snap.State != pairingAwaitingScan || snap.Code == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success":       false,
				"message":       "no pairing code available",
				"pairing_state": snap.State,
			})
			return
		}

		code, err := qr.Encode(snap.Code, qr.L)
		if err != nil {
			http.Error(w, "failed to encode QR code", http.StatusInternalServerError)
			return
		}
		code.Scale = 8
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Pairing-Seq", strconv.Itoa(snap.CodeSeq))
		w.Header().Set("X-Pairing-Issued-At", strconv.FormatInt(snap.CodeIssuedAt.Unix(), 10))
		_, _ = w.Write(code.PNG())
	}))
}
