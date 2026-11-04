package main

import (
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// offlineSyncState tracks the catch-up WhatsApp runs after a (re)connect,
// delivering the events missed while the bridge was offline. Until it
// completes messages.db lags behind the phone, so /api/health reports it.
//
// whatsmeow marks both ends exactly: OfflineSyncPreview (only sent when
// something was missed) and OfflineSyncCompleted.
type offlineSyncState struct {
	mu   sync.Mutex
	snap offlineSyncSnapshot
}

type offlineSyncSnapshot struct {
	InProgress     bool  `json:"in_progress"`
	Expected       int   `json:"expected,omitempty"`
	Messages       int   `json:"messages,omitempty"`
	Receipts       int   `json:"receipts,omitempty"`
	Notifications  int   `json:"notifications,omitempty"`
	AppDataChanges int   `json:"app_data_changes,omitempty"`
	StartedAt      int64 `json:"started_at,omitempty"`
	CompletedAt    int64 `json:"completed_at,omitempty"`
	Count          int   `json:"count,omitempty"`
}

// bridgeOfflineSync is shared by the event handler and /api/health.
var bridgeOfflineSync = &offlineSyncState{}

func (s *offlineSyncState) preview(v *events.OfflineSyncPreview) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = offlineSyncSnapshot{
		InProgress:     true,
		Expected:       v.Total,
		Messages:       v.Messages,
		Receipts:       v.Receipts,
		Notifications:  v.Notifications,
		AppDataChanges: v.AppDataChanges,
		StartedAt:      time.Now().Unix(),
	}
}

// completed keeps the preview's totals so callers can see what was caught up.
func (s *offlineSyncState) completed(v *events.OfflineSyncCompleted) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap.InProgress = false
	s.snap.CompletedAt = time.Now().Unix()
	s.snap.Count = v.Count
}

// reset drops the state when the connection goes away, so a catch-up cut
// short never stays "in progress"; the next connect starts a fresh one.
func (s *offlineSyncState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = offlineSyncSnapshot{}
}

func (s *offlineSyncState) snapshot() offlineSyncSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}
