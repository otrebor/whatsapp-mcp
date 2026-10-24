package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// newAccountTestStore returns a message store with one chat, message and call,
// and a store directory laid out like the bridge's: session files plus a media
// directory per chat.
func newAccountTestStore(t *testing.T) (*MessageStore, string) {
	t.Helper()
	ms := newTestMessageStore(t)
	if err := ensureMetaTable(ms.db); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, q := range []struct {
		sql  string
		args []interface{}
	}{
		{`INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)`, []interface{}{"111@s.whatsapp.net", "Old friend", now}},
		{`INSERT INTO messages (id, chat_jid, content, timestamp) VALUES (?, ?, ?, ?)`, []interface{}{"m1", "111@s.whatsapp.net", "hi", now}},
		{`INSERT INTO calls (call_id, chat_jid, timestamp) VALUES (?, ?, ?)`, []interface{}{"c1", "111@s.whatsapp.net", now}},
	} {
		if _, err := ms.db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	for _, f := range []string{"messages.db", "whatsapp.db", ".bridge-token"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"111@s.whatsapp.net", "120363000000000000@g.us", "not-media"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, d, "image_1.jpg"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return ms, dir
}

func historyRows(t *testing.T, ms *MessageStore) int {
	t.Helper()
	total := 0
	for _, table := range []string{"chats", "messages", "calls"} {
		var n int
		if err := ms.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		total += n
	}
	return total
}

func doRequest(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, nil)
	req.Header.Set("Authorization", "Bearer "+pairingTestToken)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestOnAccountLinked(t *testing.T) {
	alice := accountOwner{Phone: "111", LID: "900"}
	cases := []struct {
		name      string
		prev      *accountOwner // nil: no owner recorded
		next      accountOwner
		wipe      bool
		wantWiped bool
		wantOwner accountOwner
	}{
		{"no owner recorded (upgrade)", nil, accountOwner{Phone: "222", LID: "800"}, true, false, accountOwner{"222", "800"}},
		{"same account re-links", &alice, alice, true, false, alice},
		{"different account", &alice, accountOwner{Phone: "222", LID: "800"}, true, true, accountOwner{"222", "800"}},
		{"different account, LIDs unknown", &accountOwner{Phone: "111"}, accountOwner{Phone: "222"}, true, true, accountOwner{Phone: "222"}},
		{"number change keeps LID", &alice, accountOwner{Phone: "333", LID: "900"}, true, false, accountOwner{"333", "900"}},
		{"recycled number, new account", &alice, accountOwner{Phone: "111", LID: "700"}, true, true, accountOwner{"111", "700"}},
		{"same phone, new LID unknown", &alice, accountOwner{Phone: "111"}, true, false, alice},
		{"different account, wipe disabled", &alice, accountOwner{Phone: "222", LID: "800"}, false, false, accountOwner{"222", "800"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ms, dir := newAccountTestStore(t)
			if c.prev != nil {
				if err := ms.saveOwner(*c.prev); err != nil {
					t.Fatal(err)
				}
			}

			wiped, err := ms.onAccountLinked(c.next, c.wipe, dir, waLog.Noop)
			if err != nil {
				t.Fatal(err)
			}

			if wiped != c.wantWiped {
				t.Errorf("wiped = %v, want %v", wiped, c.wantWiped)
			}
			rows := historyRows(t, ms)
			if c.wantWiped && rows != 0 {
				t.Errorf("%d history rows left after wipe", rows)
			}
			if !c.wantWiped && rows != 3 {
				t.Errorf("history rows = %d, want 3 kept", rows)
			}
			if got := exists(filepath.Join(dir, "111@s.whatsapp.net")); got == c.wantWiped {
				t.Errorf("media dir exists = %v, want %v", got, !c.wantWiped)
			}
			if _, ok := ms.historyWipedAt(); ok != c.wantWiped {
				t.Errorf("history_wiped_at recorded = %v, want %v", ok, c.wantWiped)
			}
			owner, err := ms.loadOwner()
			if err != nil {
				t.Fatal(err)
			}
			if owner != c.wantOwner {
				t.Errorf("owner = %+v, want %+v", owner, c.wantOwner)
			}
		})
	}
}

func TestWipeHistoryKeepsSessionFiles(t *testing.T) {
	ms, dir := newAccountTestStore(t)
	if err := ms.wipeHistory(dir); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"messages.db", "whatsapp.db", ".bridge-token", "not-media"} {
		if !exists(filepath.Join(dir, keep)) {
			t.Errorf("%s was removed", keep)
		}
	}
	for _, gone := range []string{"111@s.whatsapp.net", "120363000000000000@g.us"} {
		if exists(filepath.Join(dir, gone)) {
			t.Errorf("media dir %s survived", gone)
		}
	}
}

func TestRecordOwnerIfMissing(t *testing.T) {
	ms, _ := newAccountTestStore(t)
	if err := ms.recordOwnerIfMissing(accountOwner{Phone: "111", LID: "900"}); err != nil {
		t.Fatal(err)
	}
	if err := ms.recordOwnerIfMissing(accountOwner{Phone: "222", LID: "800"}); err != nil {
		t.Fatal(err)
	}
	owner, err := ms.loadOwner()
	if err != nil {
		t.Fatal(err)
	}
	if owner != (accountOwner{Phone: "111", LID: "900"}) {
		t.Errorf("owner = %+v, want the first account kept", owner)
	}
}

func TestHealth_ReportsHistoryWipe(t *testing.T) {
	withPairingState(t, pairingAwaitingScan)
	ms, dir := newAccountTestStore(t)
	handler := newRESTMux(newSessionHolder(newTestClient(&mockLIDStore{})), ms, 8080, pairingTestToken, nil)
	health := func() map[string]interface{} {
		resp := doRequest(handler, http.MethodGet, "/api/health")
		var body map[string]interface{}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	if _, ok := health()["history_wiped_at"]; ok {
		t.Error("history_wiped_at reported before any wipe")
	}
	if err := ms.saveOwner(accountOwner{Phone: "111"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ms.onAccountLinked(accountOwner{Phone: "222"}, true, dir, waLog.Noop); err != nil {
		t.Fatal(err)
	}
	if _, ok := health()["history_wiped_at"].(float64); !ok {
		t.Errorf("history_wiped_at missing after wipe: %v", health())
	}
}
