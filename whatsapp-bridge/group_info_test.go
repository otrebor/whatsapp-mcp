package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestParseParticipantJIDs(t *testing.T) {
	got, err := parseParticipantJIDs([]string{"+41791234567", " 12025551234@s.whatsapp.net ", "36331245826165@lid"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"41791234567@s.whatsapp.net", "12025551234@s.whatsapp.net", "36331245826165@lid"}
	for i, w := range want {
		if got[i].String() != w {
			t.Errorf("participant %d = %q, want %q", i, got[i].String(), w)
		}
	}
}

func TestParseParticipantJIDsRejectsNonUsers(t *testing.T) {
	for _, bad := range []string{"120363430951942356@g.us", "", "+"} {
		if _, err := parseParticipantJIDs([]string{bad}); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestParseGroupJID(t *testing.T) {
	if jid, ok := parseGroupJID("120363430951942356"); !ok || jid.String() != "120363430951942356@g.us" {
		t.Errorf("bare ID = %v %v, want 120363430951942356@g.us", jid, ok)
	}
	for _, bad := range []string{"", "41791234567@s.whatsapp.net", "1@lid"} {
		if _, ok := parseGroupJID(bad); ok {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

// newSelfClient is a disconnected client linked as 41790000000 / LID 900.
func newSelfClient() *sessionHolder {
	c := newTestClientWithSelf(&mockLIDStore{}, types.NewJID("41790000000", types.DefaultUserServer))
	c.Store.LID = types.NewJID("900", types.HiddenUserServer)
	return newSessionHolder(c)
}

func TestIsOwnAccount(t *testing.T) {
	client := newSelfClient().current()
	cases := map[string]bool{
		"41790000000@s.whatsapp.net":   true,
		"41790000000:5@s.whatsapp.net": true,
		"900@lid":                      true,
		"41791111111@s.whatsapp.net":   false,
		"901@lid":                      false,
		"41790000000@g.us":             false,
	}
	for raw, want := range cases {
		jid, _ := types.ParseJID(raw)
		if got := isOwnAccount(client, jid); got != want {
			t.Errorf("isOwnAccount(%s) = %v, want %v", raw, got, want)
		}
	}
	if isOwnAccount(client, types.EmptyJID) {
		t.Error("empty JID matched the own account")
	}
}

// Validation runs before the connection check, so a disconnected client
// returns 503 only for otherwise valid requests.
func TestGroupMembershipEndpointValidation(t *testing.T) {
	identity := func(h http.HandlerFunc) http.HandlerFunc { return h }
	mux := http.NewServeMux()
	registerGroupInfoEndpoint(mux, identity, newSelfClient())

	cases := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantMsg    string
	}{
		{"remove: bad group", "/api/group-participants-remove", `{"jid":"1@s.whatsapp.net","participants":["41791111111"]}`, http.StatusBadRequest, "group JID"},
		{"remove: no participants", "/api/group-participants-remove", `{"jid":"123","participants":[]}`, http.StatusBadRequest, "at least one"},
		{"remove: self by phone", "/api/group-participants-remove", `{"jid":"123","participants":["41791111111","+41790000000"]}`, http.StatusBadRequest, "cannot remove yourself"},
		{"remove: self by LID", "/api/group-participants-remove", `{"jid":"123","participants":["900@lid"]}`, http.StatusBadRequest, "cannot remove yourself"},
		{"remove: valid but offline", "/api/group-participants-remove", `{"jid":"123","participants":["41791111111"]}`, http.StatusServiceUnavailable, "Not connected"},
		{"add: self allowed through to connection check", "/api/group-participants-add", `{"jid":"123","participants":["41790000000"]}`, http.StatusServiceUnavailable, "Not connected"},
		{"leave: bad group", "/api/group-leave", `{"jid":"41791111111@s.whatsapp.net"}`, http.StatusBadRequest, "group JID"},
		{"leave: invalid json", "/api/group-leave", `{nope`, http.StatusBadRequest, "Invalid request"},
		{"leave: valid but offline", "/api/group-leave", `{"jid":"123"}`, http.StatusServiceUnavailable, "Not connected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Errorf("body %s does not mention %q", rec.Body, tc.wantMsg)
			}
		})
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/group-leave", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/group-leave = %d, want 405", rec.Code)
	}
}
