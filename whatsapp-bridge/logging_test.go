package main

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func withTraceLogging(t *testing.T, on bool) {
	t.Helper()
	old := traceLogging
	traceLogging = on
	t.Cleanup(func() { traceLogging = old })
}

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"", logLevelInfo, true},
		{"info", logLevelInfo, true},
		{" Trace ", logLevelTrace, true},
		{"DEBUG", logLevelDebug, true},
		{"warn", logLevelWarn, true},
		{"error", logLevelError, true},
		{"verbose", logLevelInfo, false},
	}
	for _, c := range cases {
		got, ok := parseLogLevel(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseLogLevel(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestWaLogLevel_TraceMapsToDebug(t *testing.T) {
	old := bridgeLogLevel
	t.Cleanup(func() { bridgeLogLevel = old })
	for in, want := range map[string]string{
		logLevelTrace: logLevelDebug,
		logLevelDebug: logLevelDebug,
		logLevelInfo:  logLevelInfo,
		logLevelWarn:  logLevelWarn,
	} {
		bridgeLogLevel = in
		if got := waLogLevel(); got != want {
			t.Errorf("waLogLevel() at %s = %s, want %s", in, got, want)
		}
	}
}

var redactTag = regexp.MustCompile(`^#[0-9a-f]{6}(@[a-z.]+)?$`)

func TestRedact_HidesIdentifiers(t *testing.T) {
	withTraceLogging(t, false)

	for _, in := range []string{"15551234567@s.whatsapp.net", "123456789@lid", "120363000000000000@g.us", "Alice Example"} {
		got := redact(in)
		if !redactTag.MatchString(got) {
			t.Errorf("redact(%q) = %q, want a #xxxxxx tag", in, got)
		}
		if strings.Contains(got, strings.Split(in, "@")[0]) {
			t.Errorf("redact(%q) = %q leaks the identifier", in, got)
		}
		if redact(in) != got {
			t.Errorf("redact(%q) is not stable within a run", in)
		}
	}
	if got := redact("15551234567@s.whatsapp.net"); !strings.HasSuffix(got, "@s.whatsapp.net") {
		t.Errorf("JID server dropped: %q", got)
	}
	if got := redact("120363000000000000@g.us"); !strings.HasSuffix(got, "@g.us") {
		t.Errorf("group server dropped: %q", got)
	}
	if redact("111@s.whatsapp.net") == redact("222@s.whatsapp.net") {
		t.Error("different users got the same tag")
	}
	if redact("") != "" {
		t.Error("empty input should stay empty")
	}
}

func TestRedactHelpers_TraceShowsEverything(t *testing.T) {
	withTraceLogging(t, true)
	if got := redact("15551234567@s.whatsapp.net"); got != "15551234567@s.whatsapp.net" {
		t.Errorf("redact at TRACE = %q", got)
	}
	if got := redactText("hello"); got != "hello" {
		t.Errorf("redactText at TRACE = %q", got)
	}
	p := filepath.Join("store", "15551234567@s.whatsapp.net", "image.jpg")
	if got := redactPath(p); got != p {
		t.Errorf("redactPath at TRACE = %q", got)
	}
}

func TestRedactTextAndPath(t *testing.T) {
	withTraceLogging(t, false)
	if got := redactText("ciao 👋"); got != "<6 chars>" {
		t.Errorf("redactText = %q", got)
	}
	p := filepath.Join("store", "15551234567@s.whatsapp.net", "image_20260101_120000_ABC.jpg")
	if got := redactPath(p); got != "image_20260101_120000_ABC.jpg" {
		t.Errorf("redactPath = %q", got)
	}
}

func TestMessageLogSummary(t *testing.T) {
	if got := messageLogSummary(false, "3A1", ""); got != "message received: id=3A1 type=text" {
		t.Errorf("got %q", got)
	}
	if got := messageLogSummary(true, "3A2", "image"); got != "message sent: id=3A2 type=image" {
		t.Errorf("got %q", got)
	}
}

// captureStdout runs f and returns what it printed.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	f()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestTracef_OnlyAtTrace(t *testing.T) {
	withTraceLogging(t, false)
	if out := captureStdout(t, func() { tracef("secret %s\n", "x") }); out != "" {
		t.Errorf("tracef printed at INFO: %q", out)
	}
	withTraceLogging(t, true)
	if out := captureStdout(t, func() { tracef("secret %s\n", "x") }); out != "[TRACE] secret x\n" {
		t.Errorf("tracef at TRACE = %q", out)
	}
}

func TestLogLevelStartupMessage(t *testing.T) {
	withTraceLogging(t, false)
	if msg := logLevelStartupMessage(); !strings.Contains(msg, "redacted") {
		t.Errorf("INFO message = %q", msg)
	}
	withTraceLogging(t, true)
	if msg := logLevelStartupMessage(); !strings.Contains(msg, "WILL be logged") {
		t.Errorf("TRACE message = %q", msg)
	}
}
