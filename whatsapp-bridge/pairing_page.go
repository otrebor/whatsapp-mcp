package main

import (
	"embed"
	"net/http"
)

// The live pairing page (GET /pairing) polls /api/health and
// /api/pairing/qr.png, so the user always sees the current code instead of a
// snapshot that expires in ~20s. The page, script and stylesheet hold no
// secrets and are served without the bearer token; the script sends the
// token (taken from the URL fragment) on its API calls like any other client.
//
//go:embed web/pairing.html web/pairing.js web/pairing.css
var pairingPageFiles embed.FS

// pairingPageCSP allows only this origin's script, stylesheet and API, plus
// blob: images for the QR code. No inline code, no framing.
const pairingPageCSP = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' blob:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func registerPairingPage(mux *http.ServeMux, allowedHosts map[string]struct{}) {
	mux.HandleFunc("/pairing", pairingPageAsset(allowedHosts, "web/pairing.html", "text/html; charset=utf-8"))
	mux.HandleFunc("/pairing/app.js", pairingPageAsset(allowedHosts, "web/pairing.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("/pairing/app.css", pairingPageAsset(allowedHosts, "web/pairing.css", "text/css; charset=utf-8"))
}

func pairingPageAsset(allowedHosts map[string]struct{}, name, contentType string) http.HandlerFunc {
	body, err := pairingPageFiles.ReadFile(name)
	if err != nil {
		panic("pairing page asset missing from embed: " + name)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// Same DNS-rebinding guard as the API, minus the token.
		if !hostAllowed(r.Host, allowedHosts) {
			http.Error(w, "Forbidden: host not allowed", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("Content-Type", contentType)
		h.Set("Content-Security-Policy", pairingPageCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	}
}
