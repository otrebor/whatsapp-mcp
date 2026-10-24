package main

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// fakeSessionClient stands in for *whatsmeow.Client in sessionManager tests.
// qr scripts each GetQRChannel call; calls past the end block until ctx ends.
type fakeSessionClient struct {
	mu          sync.Mutex
	connectErr  error
	connects    int
	disconnects int
	qrCalls     int
	qr          [][]whatsmeow.QRChannelItem
}

func (f *fakeSessionClient) Connect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connects++
	return f.connectErr
}

func (f *fakeSessionClient) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnects++
}

func (f *fakeSessionClient) IsConnected() bool { return false }

func (f *fakeSessionClient) GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.qrCalls++
	ch := make(chan whatsmeow.QRChannelItem, 4)
	if f.qrCalls <= len(f.qr) {
		for _, item := range f.qr[f.qrCalls-1] {
			ch <- item
		}
		close(ch)
		return ch, nil
	}
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func (f *fakeSessionClient) counts() (connects, disconnects, qrCalls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects, f.disconnects, f.qrCalls
}

// testManager builds a sessionManager whose newSession hands out sessions in
// order and records the afterLogout flag of each call.
type testManager struct {
	*sessionManager
	mu          sync.Mutex
	afterLogout []bool
	gone        []types.JID
}

func newTestManager(t *testing.T, next []*waSession, deviceGone func(n int) bool) *testManager {
	t.Helper()
	tm := &testManager{}
	tm.sessionManager = &sessionManager{
		newSession: func(afterLogout bool) (*waSession, error) {
			tm.mu.Lock()
			defer tm.mu.Unlock()
			tm.afterLogout = append(tm.afterLogout, afterLogout)
			if len(tm.afterLogout) > len(next) {
				t.Errorf("unexpected session #%d", len(tm.afterLogout))
				return newTestSession(&fakeSessionClient{}, false), nil
			}
			return next[len(tm.afterLogout)-1], nil
		},
		deviceGone: func(ctx context.Context, jid types.JID) (bool, error) {
			tm.mu.Lock()
			defer tm.mu.Unlock()
			tm.gone = append(tm.gone, jid)
			return deviceGone(len(tm.gone)), nil
		},
		renderQR:        func(code string, w io.Writer) { bridgePairing.setCode(code) },
		holder:          newSessionHolder(nil),
		logger:          waLog.Noop,
		out:             io.Discard,
		maxPairAttempts: 2,
		qrTimeout:       time.Minute,
		retryDelay:      time.Millisecond,
		deleteWait:      200 * time.Millisecond,
		deletePoll:      time.Millisecond,
		reconnectDelay:  time.Millisecond,
		maxReconnect:    4 * time.Millisecond,
	}
	return tm
}

func (tm *testManager) sessionsStarted() []bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return append([]bool(nil), tm.afterLogout...)
}

func (tm *testManager) goneChecks() int {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return len(tm.gone)
}

func newTestSession(client sessionClient, paired bool) *waSession {
	s := &waSession{client: client, signals: newSessionSignals(), paired: paired}
	if paired {
		s.signals.setOwnJID(types.NewJID("15551234567", types.DefaultUserServer))
	}
	return s
}

// startManager runs the manager in the background; the returned stop
// cancels it and waits for run to return.
func startManager(t *testing.T, m *sessionManager, first *waSession) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.run(ctx, first)
		close(done)
	}()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("sessionManager.run did not return after cancel")
		}
	}
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return stop
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func pairedQR() [][]whatsmeow.QRChannelItem {
	return [][]whatsmeow.QRChannelItem{{
		{Event: "code", Code: "new-code"},
		whatsmeow.QRChannelSuccess,
	}}
}

func TestSessionManager_RelinksAfterLogoutWithoutExiting(t *testing.T) {
	p := withPairingState(t, pairingStarting)
	oldClient := &fakeSessionClient{}
	first := newTestSession(oldClient, true)
	newClient := &fakeSessionClient{qr: pairedQR()}
	second := newTestSession(newClient, false)
	tm := newTestManager(t, []*waSession{second}, func(int) bool { return true })
	stop := startManager(t, tm.sessionManager, first)

	eventually(t, "stored session to connect", func() bool {
		c, _, _ := oldClient.counts()
		return c == 1 && p.snapshot().State == pairingPaired
	})

	// What the event handler does on events.LoggedOut.
	p.setState(pairingLoggedOut, "logged out: 401")
	notify(first.signals.loggedOut)

	eventually(t, "new session to pair", func() bool {
		_, _, qr := newClient.counts()
		return qr == 1 && p.snapshot().State == pairingPaired
	})
	if got := tm.sessionsStarted(); len(got) != 1 || !got[0] {
		t.Errorf("newSession calls = %v, want one with afterLogout=true", got)
	}
	if _, d, _ := oldClient.counts(); d == 0 {
		t.Error("old client was not disconnected")
	}
	if snap := p.snapshot(); snap.CodeSeq != 1 {
		t.Errorf("CodeSeq = %d, want 1 (new code published)", snap.CodeSeq)
	}
	stop()
}

func TestSessionManager_WaitsForDeviceDeletionBeforeNewSession(t *testing.T) {
	withPairingState(t, pairingStarting)
	first := newTestSession(&fakeSessionClient{}, true)
	second := newTestSession(&fakeSessionClient{qr: pairedQR()}, false)
	tm := newTestManager(t, []*waSession{second}, func(n int) bool { return n >= 3 })
	startManager(t, tm.sessionManager, first)

	notify(first.signals.loggedOut)
	eventually(t, "new session", func() bool { return len(tm.sessionsStarted()) == 1 })
	if n := tm.goneChecks(); n != 3 {
		t.Errorf("deviceGone calls = %d, want 3 (new session only after the row is gone)", n)
	}
	if got := tm.gone[0]; got.User != "15551234567" {
		t.Errorf("deviceGone checked %v, want the logged-out device", got)
	}
}

func TestSessionManager_DeviceDeletionWaitIsBounded(t *testing.T) {
	withPairingState(t, pairingStarting)
	first := newTestSession(&fakeSessionClient{}, true)
	second := newTestSession(&fakeSessionClient{qr: pairedQR()}, false)
	tm := newTestManager(t, []*waSession{second}, func(int) bool { return false })
	startManager(t, tm.sessionManager, first)

	notify(first.signals.loggedOut)
	eventually(t, "new session despite stale device row", func() bool { return len(tm.sessionsStarted()) == 1 })
}

func TestSessionManager_LogoutRequestSkipsDeletionWait(t *testing.T) {
	withPairingState(t, pairingStarting)
	first := newTestSession(&fakeSessionClient{}, true)
	second := newTestSession(&fakeSessionClient{qr: pairedQR()}, false)
	tm := newTestManager(t, []*waSession{second}, func(int) bool { return false })
	startManager(t, tm.sessionManager, first)

	eventually(t, "stored session to connect", func() bool {
		c, _, _ := first.client.(*fakeSessionClient).counts()
		return c == 1
	})
	notify(tm.holder.logoutRequested)
	eventually(t, "new session", func() bool { return len(tm.sessionsStarted()) == 1 })
	if n := tm.goneChecks(); n != 0 {
		t.Errorf("deviceGone calls = %d, want 0 (Logout already deleted the device)", n)
	}
}

func TestSessionManager_QRTimeoutWaitsForPairingStart(t *testing.T) {
	p := withPairingState(t, pairingStarting)
	timedOut := []whatsmeow.QRChannelItem{{Event: "code", Code: "c"}, whatsmeow.QRChannelTimeout}
	client := &fakeSessionClient{qr: [][]whatsmeow.QRChannelItem{
		timedOut, timedOut, // maxPairAttempts = 2
		{{Event: "code", Code: "c2"}, whatsmeow.QRChannelSuccess},
	}}
	tm := newTestManager(t, nil, func(int) bool { return true })
	stop := startManager(t, tm.sessionManager, newTestSession(client, false))

	eventually(t, "timed_out after the last attempt", func() bool {
		_, _, qr := client.counts()
		return qr == 2 && p.snapshot().State == pairingTimedOut
	})
	time.Sleep(20 * time.Millisecond)
	if _, _, qr := client.counts(); qr != 2 {
		t.Fatalf("GetQRChannel calls = %d while idle, want 2 (no codes nobody asked for)", qr)
	}

	notify(tm.holder.pairingStart)
	eventually(t, "pairing after /api/pairing/start", func() bool {
		_, _, qr := client.counts()
		return qr == 3 && p.snapshot().State == pairingPaired
	})
	stop()
}

func TestSessionManager_ShutdownDuringPairing(t *testing.T) {
	withPairingState(t, pairingStarting)
	client := &fakeSessionClient{} // GetQRChannel blocks until ctx ends
	tm := newTestManager(t, nil, func(int) bool { return true })
	stop := startManager(t, tm.sessionManager, newTestSession(client, false))

	eventually(t, "QR channel requested", func() bool {
		_, _, qr := client.counts()
		return qr == 1
	})
	stop()
	if got := tm.sessionsStarted(); len(got) != 0 {
		t.Errorf("newSession calls after shutdown = %v, want none", got)
	}
}

func TestSessionManager_ReconnectLoopStopsOnDeletedDevice(t *testing.T) {
	client := &fakeSessionClient{connectErr: store.ErrDeviceDeleted}
	s := newTestSession(client, true)
	tm := newTestManager(t, nil, func(int) bool { return true })

	done := make(chan struct{})
	go func() {
		tm.reconnectLoop(context.Background(), s)
		close(done)
	}()
	notify(s.signals.reconnect)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnectLoop kept running on a deleted device")
	}
	if c, _, _ := client.counts(); c != 1 {
		t.Errorf("Connect calls = %d, want 1", c)
	}
}

func TestSessionManager_ReconnectLoopRetriesOtherErrors(t *testing.T) {
	client := &fakeSessionClient{connectErr: io.ErrUnexpectedEOF}
	s := newTestSession(client, true)
	tm := newTestManager(t, nil, func(int) bool { return true })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tm.reconnectLoop(ctx, s)
		close(done)
	}()
	notify(s.signals.reconnect)
	eventually(t, "retries", func() bool {
		c, _, _ := client.counts()
		return c >= 3
	})
	cancel()
	<-done
}

func TestSessionHolder_SwapIsRaceFree(t *testing.T) {
	a := newTestClient(&mockLIDStore{})
	b := newTestClient(&mockLIDStore{})
	h := newSessionHolder(a)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				if c := h.current(); c != a && c != b {
					t.Error("current returned an unknown client")
					return
				}
			}
		}()
	}
	for j := 0; j < 1000; j++ {
		if j%2 == 0 {
			h.set(b)
		} else {
			h.set(a)
		}
	}
	wg.Wait()
	h.set(b)
	if h.current() != b {
		t.Error("current did not return the last set client")
	}
}
