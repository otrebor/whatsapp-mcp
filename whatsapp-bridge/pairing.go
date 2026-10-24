package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
//
// A var so tests can stand in for a live connection.
var whatsappReady = func(client *whatsmeow.Client) bool {
	return client.IsConnected() && client.IsLoggedIn()
}

// logoutClient unlinks this device from the phone and deletes it locally. A
// var so tests can fake it.
var logoutClient = func(ctx context.Context, client *whatsmeow.Client) error {
	return client.Logout(ctx)
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

func writePairingJSON(w http.ResponseWriter, status int, body map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// registerPairingEndpoints serves the live pairing QR code as a PNG and lets
// callers log out or restart pairing without restarting the bridge.
func registerPairingEndpoints(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, sessions *sessionHolder) {
	mux.HandleFunc("/api/logout", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		client := sessions.current()
		if client == nil || client.Store == nil || client.Store.ID == nil {
			writePairingJSON(w, http.StatusConflict, map[string]interface{}{
				"success": false,
				"message": "not logged in",
			})
			return
		}
		// Logout sends the unlink request over the live connection; offline,
		// it would fail after the local session is already doubtful.
		if !requireConnected(w, client) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := logoutClient(ctx, client); err != nil {
			if errors.Is(err, whatsmeow.ErrNotLoggedIn) {
				writePairingJSON(w, http.StatusConflict, map[string]interface{}{
					"success": false,
					"message": "not logged in",
				})
				return
			}
			writePairingJSON(w, http.StatusBadGateway, map[string]interface{}{
				"success": false,
				"message": fmt.Sprintf("logout failed: %v", err),
			})
			return
		}
		bridgePairing.setState(pairingLoggedOut, "logged out via /api/logout")
		notify(sessions.logoutRequested)
		writePairingJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"message": "logged out; a new QR code will be available shortly",
		})
	}))

	mux.HandleFunc("/api/pairing/start", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		state := bridgePairing.snapshot().State
		if state != pairingTimedOut {
			writePairingJSON(w, http.StatusConflict, map[string]interface{}{
				"success":       false,
				"message":       "pairing can only be restarted after it timed out",
				"pairing_state": state,
			})
			return
		}
		notify(sessions.pairingStart)
		writePairingJSON(w, http.StatusAccepted, map[string]interface{}{
			"success":       true,
			"message":       "pairing restarted; a QR code will be available shortly",
			"pairing_state": state,
		})
	}))

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
