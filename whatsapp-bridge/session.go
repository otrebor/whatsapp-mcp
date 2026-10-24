package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// sessionHolder is the REST handlers' view of the current WhatsApp client.
//
// whatsmeow can't re-pair a client after a logout: the device is deleted and
// Connect returns store.ErrDeviceDeleted. The session manager therefore builds
// a new client per session and swaps it in here. Handlers must call current()
// once per request, never cache the result.
type sessionHolder struct {
	mu     sync.RWMutex
	client *whatsmeow.Client

	// logoutRequested is signalled by POST /api/logout after client.Logout
	// succeeded. Logout emits no events.LoggedOut, so the manager can't
	// notice on its own.
	logoutRequested chan struct{}
	// pairingStart is signalled by POST /api/pairing/start to restart QR
	// pairing after every attempt timed out.
	pairingStart chan struct{}
}

func newSessionHolder(client *whatsmeow.Client) *sessionHolder {
	return &sessionHolder{
		client:          client,
		logoutRequested: make(chan struct{}, 1),
		pairingStart:    make(chan struct{}, 1),
	}
}

func (h *sessionHolder) current() *whatsmeow.Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.client
}

func (h *sessionHolder) set(client *whatsmeow.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.client = client
}

// notify does a non-blocking send; one pending signal is enough.
func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// drain drops a stale signal left over from an earlier session.
func drain(ch chan struct{}) {
	select {
	case <-ch:
	default:
	}
}

// sessionSignals carries one session's events from its event handler to the
// manager. Each session gets its own, so a late event from a replaced client
// can't end the next session.
type sessionSignals struct {
	reconnect chan struct{}
	loggedOut chan struct{}

	mu  sync.Mutex
	jid types.JID // own device JID, for the post-logout deletion check
}

func newSessionSignals() *sessionSignals {
	return &sessionSignals{
		reconnect: make(chan struct{}, 1),
		loggedOut: make(chan struct{}, 1),
	}
}

func (s *sessionSignals) setOwnJID(jid types.JID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jid = jid
}

func (s *sessionSignals) ownJID() types.JID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jid
}

// sessionClient is the part of *whatsmeow.Client the manager drives, so tests
// can substitute a fake.
type sessionClient interface {
	Connect() error
	Disconnect()
	IsConnected() bool
	GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error)
}

// waSession is one client's lifetime, from creation to logout or shutdown.
type waSession struct {
	client  sessionClient
	signals *sessionSignals
	// paired is true when the device already had a stored session, so the
	// manager connects instead of showing a QR code.
	paired bool
}

type sessionEnd int

const (
	sessionShutdown sessionEnd = iota
	sessionLoggedOut
	sessionLogoutRequested
)

// sessionManager owns the WhatsApp connection: it connects or pairs, waits for
// a logout, and starts over with a new device, all without exiting the
// process.
type sessionManager struct {
	// newSession creates a client and publishes it to the REST handlers.
	// afterLogout asks for a brand-new device instead of the stored one.
	newSession func(afterLogout bool) (*waSession, error)
	// deviceGone reports whether jid's row has left whatsapp.db.
	deviceGone func(ctx context.Context, jid types.JID) (bool, error)
	// renderQR shows a pairing code (terminal and /api/pairing/qr.png).
	renderQR func(code string, w io.Writer)

	holder *sessionHolder
	logger waLog.Logger
	out    io.Writer

	maxPairAttempts int
	qrTimeout       time.Duration
	retryDelay      time.Duration
	deleteWait      time.Duration
	deletePoll      time.Duration
	reconnectDelay  time.Duration
	maxReconnect    time.Duration
}

// run drives sessions until ctx is cancelled, starting with first.
func (m *sessionManager) run(ctx context.Context, first *waSession) {
	s := first
	for {
		end := m.runSession(ctx, s)
		s.client.Disconnect()
		if end == sessionShutdown {
			return
		}
		if end == sessionLoggedOut {
			m.waitDeviceGone(ctx, s.signals.ownJID())
		}
		m.logger.Infof("Starting a new session; a QR code will follow")

		for {
			next, err := m.newSession(true)
			if err == nil {
				s = next
				break
			}
			m.logger.Errorf("Failed to start a new session: %v", err)
			bridgePairing.setError(fmt.Sprintf("failed to start a new session: %v", err))
			if !sleepCtx(ctx, m.retryDelay) {
				return
			}
		}
	}
}

func (m *sessionManager) runSession(ctx context.Context, s *waSession) sessionEnd {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	drain(m.holder.logoutRequested)

	if s.paired {
		bridgePairing.setState(pairingPaired, "")
		if err := s.client.Connect(); err != nil {
			m.logger.Errorf("Failed to connect: %v", err)
			bridgePairing.setError(fmt.Sprintf("failed to connect: %v", err))
			notify(s.signals.reconnect)
		}
	} else if !m.pair(sctx, s) {
		return sessionShutdown
	}

	go m.reconnectLoop(sctx, s)

	// A stored session the phone already unlinked connects and is then
	// logged out, so the logout signal is watched here too.
	select {
	case <-ctx.Done():
		return sessionShutdown
	case <-s.signals.loggedOut:
		return sessionLoggedOut
	case <-m.holder.logoutRequested:
		return sessionLogoutRequested
	}
}

// pair shows QR codes until the phone scans one. After maxPairAttempts
// timeouts it waits for POST /api/pairing/start. It returns false on shutdown.
func (m *sessionManager) pair(ctx context.Context, s *waSession) bool {
	drain(m.holder.pairingStart)
	for attempt := 1; ; attempt++ {
		m.logger.Infof("Pairing attempt %d/%d...", attempt, m.maxPairAttempts)
		if m.pairOnce(ctx, s) == pairingQRSucceeded {
			bridgePairing.setState(pairingPaired, "")
			_, _ = fmt.Fprintln(m.out, "\nSuccessfully connected and authenticated!")
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if attempt < m.maxPairAttempts {
			if !sleepCtx(ctx, m.retryDelay) {
				return false
			}
			continue
		}

		m.logger.Warnf("No QR code was scanned; POST /api/pairing/start (or get_pairing_qr) to try again")
		bridgePairing.setState(pairingTimedOut, "QR code timed out; start pairing again to get a new code")
		select {
		case <-ctx.Done():
			return false
		case <-m.holder.pairingStart:
			attempt = 0
		}
	}
}

func (m *sessionManager) pairOnce(ctx context.Context, s *waSession) pairingQROutcome {
	qctx, cancel := context.WithTimeout(ctx, m.qrTimeout)
	defer cancel()

	qrChan, err := s.client.GetQRChannel(qctx)
	if err != nil {
		m.logger.Errorf("Failed to get QR channel: %v", err)
		bridgePairing.setError(fmt.Sprintf("failed to get QR channel: %v", err))
		return pairingQRChannelClosed
	}
	if err := s.client.Connect(); err != nil {
		m.logger.Errorf("Failed to connect: %v", err)
		bridgePairing.setError(fmt.Sprintf("failed to connect: %v", err))
		return pairingQRChannelClosed
	}

	outcome := renderPairingQRCodes(qrChan, m.out, m.renderQR)
	switch {
	case outcome == pairingQRSucceeded:
	case outcome == pairingQRTimedOut:
		m.logger.Warnf("QR code timed out")
		bridgePairing.setState(pairingTimedOut, "QR code timed out")
		s.client.Disconnect()
	case qctx.Err() != nil && ctx.Err() == nil:
		m.logger.Warnf("Timed out waiting for QR code scan")
		bridgePairing.setState(pairingTimedOut, "timed out waiting for QR code scan")
		s.client.Disconnect()
	default:
		s.client.Disconnect()
	}
	return outcome
}

// reconnectLoop retries Connect with backoff when the event handler reports a
// dropped connection. It stops with its session.
func (m *sessionManager) reconnectLoop(ctx context.Context, s *waSession) {
	backoff := m.reconnectDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.signals.reconnect:
		}
		m.logger.Infof("🔄 Attempting to reconnect...")
		if !sleepCtx(ctx, backoff) {
			return
		}
		if s.client.IsConnected() {
			m.logger.Infof("Already connected, skipping reconnection")
			backoff = m.reconnectDelay
			continue
		}
		err := s.client.Connect()
		switch {
		case err == nil:
			m.logger.Infof("✓ Reconnected successfully")
			backoff = m.reconnectDelay
		case errors.Is(err, store.ErrDeviceDeleted):
			// Logged out; the manager is ending this session.
			return
		default:
			m.logger.Errorf("❌ Reconnection failed: %v", err)
			backoff *= 2
			if backoff > m.maxReconnect {
				backoff = m.maxReconnect
			}
			notify(s.signals.reconnect)
		}
	}
}

// waitDeviceGone waits until whatsmeow has deleted the logged-out device.
// whatsmeow dispatches events.LoggedOut before it calls Store.Delete, and
// reading the old Device's fields from here would race with that, so poll the
// database instead.
func (m *sessionManager) waitDeviceGone(ctx context.Context, jid types.JID) {
	if jid.IsEmpty() {
		return
	}
	deadline := time.Now().Add(m.deleteWait)
	for {
		gone, err := m.deviceGone(ctx, jid)
		if err == nil && gone {
			return
		}
		if time.Now().After(deadline) {
			m.logger.Warnf("Logged-out device %s is still in whatsapp.db (err: %v); a restart may try to reuse it", jid, err)
			return
		}
		if !sleepCtx(ctx, m.deletePoll) {
			return
		}
	}
}

// sleepCtx sleeps for d, returning false if ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
