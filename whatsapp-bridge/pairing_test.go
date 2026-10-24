package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

const pairingTestToken = "supersecrettoken1234567890abcdef"

// withPairingState swaps in a fresh bridgePairing for one test.
func withPairingState(t *testing.T, state string) *pairingState {
	t.Helper()
	old := bridgePairing
	bridgePairing = &pairingState{state: state}
	t.Cleanup(func() { bridgePairing = old })
	return bridgePairing
}

func doPairingRequest(t *testing.T, client *whatsmeow.Client, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	handler := newRESTMux(newSessionHolder(client), newTestMessageStore(t), 8080, pairingTestToken, nil)
	req := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func TestRenderPairingQRCodes_PublishesEveryCode(t *testing.T) {
	p := withPairingState(t, pairingStarting)
	qrChan := make(chan whatsmeow.QRChannelItem, 3)
	qrChan <- whatsmeow.QRChannelItem{Event: "code", Code: "first-code"}
	qrChan <- whatsmeow.QRChannelItem{Event: "code", Code: "rotated-code"}
	close(qrChan)

	renderPairingQRCodes(qrChan, io.Discard, func(code string, w io.Writer) {
		p.setCode(code)
	})

	snap := p.snapshot()
	if snap.State != pairingAwaitingScan || snap.Code != "rotated-code" || snap.CodeSeq != 2 {
		t.Fatalf("snapshot = %+v, want awaiting_scan with rotated-code at seq 2", snap)
	}
	if snap.CodeIssuedAt.IsZero() {
		t.Error("codeIssuedAt not set")
	}

	p.setState(pairingPaired, "")
	if snap := p.snapshot(); snap.Code != "" {
		t.Errorf("code kept after leaving awaiting_scan: %q", snap.Code)
	}
}

func TestPairingQR_RequiresAuth(t *testing.T) {
	withPairingState(t, pairingStarting).setCode("live-code")
	resp := doPairingRequest(t, newTestClient(&mockLIDStore{}), http.MethodGet, "/api/pairing/qr.png", "", "")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.Code)
	}
	if bytes.Contains(resp.Body.Bytes(), []byte("live-code")) {
		t.Error("unauthenticated response leaked the code")
	}
}

func TestPairingQR_NoCodeReturns404(t *testing.T) {
	for _, state := range []string{pairingStarting, pairingPaired, pairingTimedOut, pairingLoggedOut} {
		withPairingState(t, state)
		resp := doPairingRequest(t, newTestClient(&mockLIDStore{}), http.MethodGet, "/api/pairing/qr.png", "", pairingTestToken)
		if resp.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", state, resp.Code)
			continue
		}
		var body map[string]interface{}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: bad JSON: %v", state, err)
		}
		if body["pairing_state"] != state {
			t.Errorf("%s: pairing_state = %v", state, body["pairing_state"])
		}
	}
}

func TestPairingQR_ServesPNG(t *testing.T) {
	p := withPairingState(t, pairingStarting)
	p.setCode("2@first")
	p.setCode("2@second")

	resp := doPairingRequest(t, newTestClient(&mockLIDStore{}), http.MethodGet, "/api/pairing/qr.png", "", pairingTestToken)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.Code, resp.Body.String())
	}
	if !bytes.HasPrefix(resp.Body.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
		t.Error("body is not a PNG")
	}
	if got := resp.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := resp.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := resp.Header().Get("X-Pairing-Seq"); got != "2" {
		t.Errorf("X-Pairing-Seq = %q, want 2", got)
	}
}

func TestHealth_ReportsPairingFields(t *testing.T) {
	cases := []struct {
		state    string
		loggedIn bool
	}{
		{pairingAwaitingScan, false},
		{pairingTimedOut, false},
		{pairingLoggedOut, false},
		{pairingPaired, true},
	}
	for _, c := range cases {
		p := withPairingState(t, c.state)
		p.setError("last failure")
		client := newTestClient(&mockLIDStore{})
		if c.loggedIn {
			client = newTestClientWithSelf(&mockLIDStore{}, types.NewJID("15551234567", types.DefaultUserServer))
		}

		resp := doPairingRequest(t, client, http.MethodGet, "/api/health", "", pairingTestToken)

		// Test clients are never connected, so the status code stays 503.
		if resp.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", c.state, resp.Code)
		}
		var body map[string]interface{}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: bad JSON: %v", c.state, err)
		}
		if body["pairing_state"] != c.state {
			t.Errorf("%s: pairing_state = %v", c.state, body["pairing_state"])
		}
		if body["logged_in"] != c.loggedIn {
			t.Errorf("%s: logged_in = %v, want %v", c.state, body["logged_in"], c.loggedIn)
		}
		if body["connected"] != false || body["status"] != "disconnected" {
			t.Errorf("%s: existing fields changed: %v", c.state, body)
		}
		if body["pairing_error"] != "last failure" {
			t.Errorf("%s: pairing_error = %v", c.state, body["pairing_error"])
		}
	}
}

func TestRequireConnected_DisconnectedReturns503(t *testing.T) {
	cases := []struct{ path, body string }{
		{"/api/send", `{"recipient":"15551234567","message":"hi"}`},
		{"/api/react", `{"recipient":"15551234567@s.whatsapp.net","message_id":"3A1","emoji":"x"}`},
		{"/api/typing", `{"recipient":"15551234567","is_typing":true}`},
	}
	for _, c := range cases {
		resp := doPairingRequest(t, newTestClient(&mockLIDStore{}), http.MethodPost, c.path, c.body, pairingTestToken)
		if resp.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", c.path, resp.Code)
			continue
		}
		if !strings.Contains(resp.Body.String(), "not connected to WhatsApp") {
			t.Errorf("%s: body = %s", c.path, resp.Body.String())
		}
	}
}

func doSessionRequest(t *testing.T, sessions *sessionHolder, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	handler := newRESTMux(sessions, newTestMessageStore(t), 8080, pairingTestToken, nil)
	req := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

// fakeLogout makes whatsappReady report a live connection and replaces
// client.Logout, restoring both after the test.
func fakeLogout(t *testing.T, err error) *int {
	t.Helper()
	oldReady, oldLogout := whatsappReady, logoutClient
	t.Cleanup(func() { whatsappReady, logoutClient = oldReady, oldLogout })
	calls := 0
	whatsappReady = func(*whatsmeow.Client) bool { return true }
	logoutClient = func(context.Context, *whatsmeow.Client) error {
		calls++
		return err
	}
	return &calls
}

func selfClient() *whatsmeow.Client {
	return newTestClientWithSelf(&mockLIDStore{}, types.NewJID("15551234567", types.DefaultUserServer))
}

func signalled(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestLogout_RequiresAuthAndPost(t *testing.T) {
	withPairingState(t, pairingPaired)
	calls := fakeLogout(t, nil)
	sessions := newSessionHolder(selfClient())

	if resp := doSessionRequest(t, sessions, http.MethodPost, "/api/logout", ""); resp.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", resp.Code)
	}
	if resp := doSessionRequest(t, sessions, http.MethodGet, "/api/logout", pairingTestToken); resp.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status = %d, want 405", resp.Code)
	}
	if *calls != 0 {
		t.Errorf("Logout called %d times on rejected requests", *calls)
	}
}

func TestLogout_NotLoggedInReturns409(t *testing.T) {
	withPairingState(t, pairingAwaitingScan)
	calls := fakeLogout(t, nil)
	sessions := newSessionHolder(newTestClient(&mockLIDStore{}))

	resp := doSessionRequest(t, sessions, http.MethodPost, "/api/logout", pairingTestToken)
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.Code)
	}
	if *calls != 0 || signalled(sessions.logoutRequested) {
		t.Error("logout ran without a session")
	}
}

func TestLogout_OfflineReturns503(t *testing.T) {
	withPairingState(t, pairingPaired)
	calls := fakeLogout(t, nil)
	whatsappReady = func(*whatsmeow.Client) bool { return false }
	sessions := newSessionHolder(selfClient())

	resp := doSessionRequest(t, sessions, http.MethodPost, "/api/logout", pairingTestToken)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.Code)
	}
	if *calls != 0 || signalled(sessions.logoutRequested) {
		t.Error("logout ran while offline")
	}
}

func TestLogout_SuccessSignalsManager(t *testing.T) {
	p := withPairingState(t, pairingPaired)
	calls := fakeLogout(t, nil)
	sessions := newSessionHolder(selfClient())

	resp := doSessionRequest(t, sessions, http.MethodPost, "/api/logout", pairingTestToken)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.Code, resp.Body.String())
	}
	if *calls != 1 {
		t.Errorf("Logout calls = %d, want 1", *calls)
	}
	if !signalled(sessions.logoutRequested) {
		t.Error("manager was not told about the logout")
	}
	if got := p.snapshot().State; got != pairingLoggedOut {
		t.Errorf("pairing_state = %q, want logged_out", got)
	}
}

func TestLogout_FailureKeepsSession(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{whatsmeow.ErrNotLoggedIn, http.StatusConflict},
		{io.ErrUnexpectedEOF, http.StatusBadGateway},
	}
	for _, c := range cases {
		p := withPairingState(t, pairingPaired)
		fakeLogout(t, c.err)
		sessions := newSessionHolder(selfClient())

		resp := doSessionRequest(t, sessions, http.MethodPost, "/api/logout", pairingTestToken)
		if resp.Code != c.want {
			t.Errorf("%v: status = %d, want %d", c.err, resp.Code, c.want)
		}
		if signalled(sessions.logoutRequested) {
			t.Errorf("%v: manager signalled after a failed logout", c.err)
		}
		if got := p.snapshot().State; got != pairingPaired {
			t.Errorf("%v: pairing_state = %q, want paired", c.err, got)
		}
	}
}

func TestPairingStart_OnlyAfterTimeout(t *testing.T) {
	for _, state := range []string{pairingStarting, pairingAwaitingScan, pairingLoggedOut, pairingPaired} {
		withPairingState(t, state)
		sessions := newSessionHolder(newTestClient(&mockLIDStore{}))
		resp := doSessionRequest(t, sessions, http.MethodPost, "/api/pairing/start", pairingTestToken)
		if resp.Code != http.StatusConflict {
			t.Errorf("%s: status = %d, want 409", state, resp.Code)
		}
		if signalled(sessions.pairingStart) {
			t.Errorf("%s: pairing restarted", state)
		}
	}

	withPairingState(t, pairingTimedOut)
	sessions := newSessionHolder(newTestClient(&mockLIDStore{}))
	if resp := doSessionRequest(t, sessions, http.MethodPost, "/api/pairing/start", ""); resp.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", resp.Code)
	}
	resp := doSessionRequest(t, sessions, http.MethodPost, "/api/pairing/start", pairingTestToken)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("timed_out: status = %d, want 202", resp.Code)
	}
	if !signalled(sessions.pairingStart) {
		t.Error("timed_out: manager was not told to restart pairing")
	}
}

func TestHandlers_UseCurrentClientPerRequest(t *testing.T) {
	withPairingState(t, pairingPaired)
	sessions := newSessionHolder(newTestClient(&mockLIDStore{}))
	handler := newRESTMux(sessions, newTestMessageStore(t), 8080, pairingTestToken, nil)

	loggedIn := func() interface{} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/health", nil)
		req.Header.Set("Authorization", "Bearer "+pairingTestToken)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		var body map[string]interface{}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		return body["logged_in"]
	}

	if got := loggedIn(); got != false {
		t.Fatalf("logged_in = %v before swap, want false", got)
	}
	sessions.set(selfClient())
	if got := loggedIn(); got != true {
		t.Errorf("logged_in = %v after swap, want true (handler cached the old client)", got)
	}
}
