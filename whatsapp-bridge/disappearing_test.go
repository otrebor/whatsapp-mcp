package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
)

// Validation runs before the connection check, so a disconnected client
// returns 503 only for otherwise valid requests.
func TestDisappearingTimerEndpointValidation(t *testing.T) {
	identity := func(h http.HandlerFunc) http.HandlerFunc { return h }
	mux := http.NewServeMux()
	registerDisappearingTimerEndpoint(mux, identity, newSessionHolder(newTestClient(&mockLIDStore{})), newTestMessageStore(t))

	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantMsg    string
	}{
		{"invalid json", `{nope`, http.StatusBadRequest, "Invalid request"},
		{"missing chat", `{"duration":"7d"}`, http.StatusBadRequest, "chat_jid"},
		{"broadcast chat", `{"chat_jid":"status@broadcast","duration":"7d"}`, http.StatusBadRequest, "broadcast"},
		{"non-standard duration", `{"chat_jid":"123@g.us","duration":"3d"}`, http.StatusBadRequest, "off, 24h, 7d, 90d"},
		{"missing duration", `{"chat_jid":"123@g.us"}`, http.StatusBadRequest, "off, 24h, 7d, 90d"},
		{"group, valid but offline", `{"chat_jid":"123@g.us","duration":"90d"}`, http.StatusServiceUnavailable, "Not connected"},
		{"phone, valid but offline", `{"chat_jid":"+41791234567","duration":"off"}`, http.StatusServiceUnavailable, "Not connected"},
		{"lid, valid but offline", `{"chat_jid":"900@lid","duration":"24h"}`, http.StatusServiceUnavailable, "Not connected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/disappearing-timer", strings.NewReader(tc.body)))
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Errorf("body %s does not mention %q", rec.Body, tc.wantMsg)
			}
		})
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/disappearing-timer", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d, want 405", rec.Code)
	}
}

func TestDisappearingLabel(t *testing.T) {
	cases := map[string]string{"24h": "24 hours", "7d": "7 days", "90d": "90 days"}
	for in, want := range cases {
		timer, _ := whatsmeow.ParseDisappearingTimerString(in)
		if got := disappearingLabel(timer); got != want {
			t.Errorf("disappearingLabel(%s) = %q, want %q", in, got, want)
		}
	}
}
