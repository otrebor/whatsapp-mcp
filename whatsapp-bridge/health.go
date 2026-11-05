package main

import (
	"sync/atomic"
	"time"
)

// restServerErr holds the REST server's ListenAndServe failure (for example
// "address already in use"), or nil while it is serving.
var restServerErr atomic.Pointer[string]

// bridgeRuntime is what an in-process UI needs from a running bridge. It is
// published once the REST server has started and cleared when runBridge
// returns.
type bridgeRuntime struct {
	sessions     *sessionHolder
	messageStore *MessageStore
	port         int
	token        string
}

var currentRuntime atomic.Pointer[bridgeRuntime]

// healthSnapshot is the bridge's current state as reported by /api/health.
// It is computed in one place so other in-process views of the bridge (see
// docs/plans/tray-ui.md) report exactly what the REST endpoint does.
type healthSnapshot struct {
	Connected      bool
	LoggedIn       bool
	PairingState   string
	PairingError   string
	OfflineSync    offlineSyncSnapshot
	HistoryWipedAt time.Time // zero when history was never wiped
	Timestamp      time.Time
	// RESTError is set when the REST server failed to bind. Not part of the
	// /api/health JSON: a caller that can read that JSON has a working server.
	RESTError string
}

func bridgeHealth(sessions *sessionHolder, messageStore *MessageStore) healthSnapshot {
	client := sessions.current()
	pairing := bridgePairing.snapshot()
	h := healthSnapshot{
		Connected:    whatsappReady(client),
		LoggedIn:     client.Store.ID != nil,
		PairingState: pairing.State,
		PairingError: pairing.LastError,
		OfflineSync:  bridgeOfflineSync.snapshot(),
		Timestamp:    time.Now(),
	}
	if wipedAt, ok := messageStore.historyWipedAt(); ok {
		h.HistoryWipedAt = wipedAt
	}
	if e := restServerErr.Load(); e != nil {
		h.RESTError = *e
	}
	return h
}

// healthJSON renders a snapshot as the /api/health response body.
func healthJSON(h healthSnapshot) map[string]interface{} {
	status := map[string]interface{}{
		"status":        "ok",
		"connected":     h.Connected,
		"logged_in":     h.LoggedIn,
		"pairing_state": h.PairingState,
		"offline_sync":  h.OfflineSync,
		"timestamp":     h.Timestamp.Unix(),
	}
	if h.PairingError != "" {
		status["pairing_error"] = h.PairingError
	}
	if !h.HistoryWipedAt.IsZero() {
		status["history_wiped_at"] = h.HistoryWipedAt.Unix()
	}
	if !h.Connected {
		status["status"] = "disconnected"
	}
	return status
}
