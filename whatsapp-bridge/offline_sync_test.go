package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// withOfflineSyncState swaps in a fresh bridgeOfflineSync for one test.
func withOfflineSyncState(t *testing.T) *offlineSyncState {
	t.Helper()
	old := bridgeOfflineSync
	bridgeOfflineSync = &offlineSyncState{}
	t.Cleanup(func() { bridgeOfflineSync = old })
	return bridgeOfflineSync
}

func TestOfflineSync_PreviewThenCompleted(t *testing.T) {
	s := withOfflineSyncState(t)

	s.preview(&events.OfflineSyncPreview{Total: 12, Messages: 7, Receipts: 4, Notifications: 1})
	snap := s.snapshot()
	if !snap.InProgress || snap.Expected != 12 || snap.Messages != 7 || snap.Receipts != 4 || snap.Notifications != 1 {
		t.Fatalf("after preview: %+v", snap)
	}
	if snap.StartedAt == 0 || snap.CompletedAt != 0 {
		t.Errorf("after preview: started_at=%d completed_at=%d", snap.StartedAt, snap.CompletedAt)
	}

	s.completed(&events.OfflineSyncCompleted{Count: 12})
	snap = s.snapshot()
	if snap.InProgress || snap.Count != 12 || snap.CompletedAt == 0 {
		t.Fatalf("after completed: %+v", snap)
	}
	// The preview's totals stay visible after completion.
	if snap.Expected != 12 || snap.StartedAt == 0 {
		t.Errorf("preview totals lost on completion: %+v", snap)
	}
}

func TestOfflineSync_ResetClearsInterruptedCatchUp(t *testing.T) {
	s := withOfflineSyncState(t)
	s.preview(&events.OfflineSyncPreview{Total: 5})

	s.reset()

	if snap := s.snapshot(); snap != (offlineSyncSnapshot{}) {
		t.Fatalf("after reset: %+v, want zero value", snap)
	}
}

func TestOfflineSync_EventHandlerTracksAndResets(t *testing.T) {
	s := withOfflineSyncState(t)
	handler := newEventHandler(newTestClient(&mockLIDStore{}), newTestMessageStore(t), newSessionSignals(), waLog.Noop)

	handler(&events.OfflineSyncPreview{Total: 3, Messages: 3})
	if !s.snapshot().InProgress {
		t.Fatal("preview event did not start the catch-up")
	}
	handler(&events.Disconnected{})
	if s.snapshot().InProgress {
		t.Fatal("disconnect left the catch-up in progress")
	}
}

func TestHealth_ReportsOfflineSync(t *testing.T) {
	withPairingState(t, pairingPaired)
	s := withOfflineSyncState(t)
	s.preview(&events.OfflineSyncPreview{Total: 9, Messages: 6})

	resp := doPairingRequest(t, newTestClient(&mockLIDStore{}), http.MethodGet, "/api/health", "", pairingTestToken)

	var body struct {
		OfflineSync map[string]interface{} `json:"offline_sync"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if body.OfflineSync["in_progress"] != true || body.OfflineSync["expected"] != float64(9) || body.OfflineSync["messages"] != float64(6) {
		t.Errorf("offline_sync = %v", body.OfflineSync)
	}
	if _, ok := body.OfflineSync["completed_at"]; ok {
		t.Errorf("completed_at present before completion: %v", body.OfflineSync)
	}
}
