package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func TestBridgeHealth_Snapshot(t *testing.T) {
	p := withPairingState(t, pairingPaired)
	p.setError("earlier failure")
	withOfflineSyncState(t)

	oldReady := whatsappReady
	t.Cleanup(func() { whatsappReady = oldReady })
	whatsappReady = func(*whatsmeow.Client) bool { return true }

	client := newTestClientWithSelf(&mockLIDStore{}, types.NewJID("15551234567", types.DefaultUserServer))
	h := bridgeHealth(newSessionHolder(client), newTestMessageStore(t))

	if !h.Connected || !h.LoggedIn {
		t.Errorf("connected=%v logged_in=%v, want both true", h.Connected, h.LoggedIn)
	}
	if h.PairingState != pairingPaired || h.PairingError != "earlier failure" {
		t.Errorf("pairing = %q / %q", h.PairingState, h.PairingError)
	}
	if !h.HistoryWipedAt.IsZero() {
		t.Errorf("HistoryWipedAt = %v, want zero", h.HistoryWipedAt)
	}
	if time.Since(h.Timestamp) > time.Minute {
		t.Errorf("Timestamp = %v, want now", h.Timestamp)
	}
}

// The JSON keys are part of the REST contract the MCP server reads.
func TestHealthJSON_Keys(t *testing.T) {
	keys := func(m map[string]interface{}) string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return strings.Join(ks, ",")
	}
	ts := time.Unix(1700000000, 0)

	minimal := healthJSON(healthSnapshot{Connected: true, PairingState: pairingPaired, Timestamp: ts})
	if got, want := keys(minimal), "connected,logged_in,offline_sync,pairing_state,status,timestamp"; got != want {
		t.Errorf("keys = %s, want %s", got, want)
	}
	if minimal["status"] != "ok" || minimal["timestamp"] != int64(1700000000) {
		t.Errorf("status/timestamp = %v / %v", minimal["status"], minimal["timestamp"])
	}

	full := healthJSON(healthSnapshot{
		PairingState:   pairingLoggedOut,
		PairingError:   "boom",
		HistoryWipedAt: time.Unix(1600000000, 0),
		Timestamp:      ts,
	})
	if got, want := keys(full), "connected,history_wiped_at,logged_in,offline_sync,pairing_error,pairing_state,status,timestamp"; got != want {
		t.Errorf("keys = %s, want %s", got, want)
	}
	if full["status"] != "disconnected" || full["history_wiped_at"] != int64(1600000000) {
		t.Errorf("status/history_wiped_at = %v / %v", full["status"], full["history_wiped_at"])
	}
}

func TestHealthEndpoint_ConnectedReturns200(t *testing.T) {
	withPairingState(t, pairingPaired)
	oldReady := whatsappReady
	t.Cleanup(func() { whatsappReady = oldReady })
	whatsappReady = func(*whatsmeow.Client) bool { return true }

	resp := doPairingRequest(t, newTestClient(&mockLIDStore{}), http.MethodGet, "/api/health", "", pairingTestToken)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" || body["connected"] != true {
		t.Errorf("body = %v", body)
	}
}
