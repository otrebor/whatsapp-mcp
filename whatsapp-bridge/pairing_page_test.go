package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doPageRequest(t *testing.T, method, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	handler := newRESTMux(newSessionHolder(newTestClient(&mockLIDStore{})), newTestMessageStore(t), 8080, pairingTestToken, nil)
	req := httptest.NewRequest(method, "http://"+host+path, nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func TestPairingPage_ServesAssetsWithoutToken(t *testing.T) {
	cases := []struct{ path, contentType, marker string }{
		{"/pairing", "text/html", `<script src="/pairing/app.js"`},
		{"/pairing/app.js", "text/javascript", "/pairing/qr.png"},
		{"/pairing/app.css", "text/css", ".qr"},
	}
	for _, c := range cases {
		resp := doPageRequest(t, http.MethodGet, "127.0.0.1:8080", c.path)
		if resp.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", c.path, resp.Code)
			continue
		}
		if got := resp.Header().Get("Content-Type"); !strings.HasPrefix(got, c.contentType) {
			t.Errorf("%s: Content-Type = %q, want %s", c.path, got, c.contentType)
		}
		if !strings.Contains(resp.Body.String(), c.marker) {
			t.Errorf("%s: body lacks %q", c.path, c.marker)
		}
	}
}

func TestPairingPage_OffersUnlink(t *testing.T) {
	html := doPageRequest(t, http.MethodGet, "127.0.0.1:8080", "/pairing").Body.String()
	if !strings.Contains(html, `id="unlink"`) {
		t.Error("page has no unlink button")
	}
	js := doPageRequest(t, http.MethodGet, "127.0.0.1:8080", "/pairing/app.js").Body.String()
	if !strings.Contains(js, `api("/logout", { method: "POST" })`) {
		t.Error("script does not call POST /api/logout")
	}
	if !strings.Contains(js, "confirm(") {
		t.Error("unlink is not confirmed before it runs")
	}
}

func TestPairingPage_SecurityHeaders(t *testing.T) {
	resp := doPageRequest(t, http.MethodGet, "localhost:8080", "/pairing")
	h := resp.Header()
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'", "img-src 'self' blob:"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP allows inline code: %q", csp)
	}
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store",
		"X-Frame-Options":        "DENY",
	} {
		if got := h.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestPairingPage_HoldsNoSecrets(t *testing.T) {
	withPairingState(t, pairingStarting).setCode("live-code-must-not-leak")
	for _, path := range []string{"/pairing", "/pairing/app.js", "/pairing/app.css"} {
		body := doPageRequest(t, http.MethodGet, "127.0.0.1:8080", path).Body.String()
		if strings.Contains(body, pairingTestToken) || strings.Contains(body, "live-code-must-not-leak") {
			t.Errorf("%s leaks the token or the pairing code", path)
		}
	}
}

func TestPairingPage_RejectsForeignHost(t *testing.T) {
	resp := doPageRequest(t, http.MethodGet, "evil.example:8080", "/pairing")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (DNS-rebinding guard)", resp.Code)
	}
}

func TestPairingPage_RejectsWrites(t *testing.T) {
	resp := doPageRequest(t, http.MethodPost, "127.0.0.1:8080", "/pairing")
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.Code)
	}
}
