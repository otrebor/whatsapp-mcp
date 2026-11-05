package main

// Privacy-aware logging.
//
// By default the bridge log must not contain message content or who a
// message was sent to or received from. Phone numbers, JIDs, contact/group
// names and message text are only written at WHATSAPP_LOG_LEVEL=TRACE. At
// other levels identifiers are replaced by redact(), a short salted hash
// that lets you follow one chat through a single run's log without
// revealing it.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Log levels for WHATSAPP_LOG_LEVEL, least to most verbose.
const (
	logLevelError = "ERROR"
	logLevelWarn  = "WARN"
	logLevelInfo  = "INFO"
	logLevelDebug = "DEBUG" // adds whatsmeow's protocol debug output, which contains JIDs
	logLevelTrace = "TRACE" // adds message content and identifiers in the bridge's own lines
)

// parseLogLevel normalizes WHATSAPP_LOG_LEVEL. Unknown or empty values fall
// back to INFO; ok is false for a non-empty unknown value.
func parseLogLevel(v string) (level string, ok bool) {
	switch l := strings.ToUpper(strings.TrimSpace(v)); l {
	case logLevelError, logLevelWarn, logLevelInfo, logLevelDebug, logLevelTrace:
		return l, true
	case "":
		return logLevelInfo, true
	default:
		return logLevelInfo, false
	}
}

var (
	bridgeLogLevel, bridgeLogLevelValid = parseLogLevel(os.Getenv("WHATSAPP_LOG_LEVEL"))
	// traceLogging is a var so tests can switch it.
	traceLogging = bridgeLogLevel == logLevelTrace
)

// waLogLevel is the level for the waLog loggers shared with whatsmeow, which
// has no TRACE level.
func waLogLevel() string {
	if bridgeLogLevel == logLevelTrace {
		return logLevelDebug
	}
	return bridgeLogLevel
}

// logLevelStartupMessage describes the active log level for the startup log.
func logLevelStartupMessage() string {
	msg := fmt.Sprintf("Log level %s", bridgeLogLevel)
	if !bridgeLogLevelValid {
		msg += fmt.Sprintf(" (unknown WHATSAPP_LOG_LEVEL=%q ignored)", os.Getenv("WHATSAPP_LOG_LEVEL"))
	}
	if traceLogging {
		return msg + ": message content, phone numbers and names WILL be logged"
	}
	return msg + ": message content and contacts are redacted (WHATSAPP_LOG_LEVEL=TRACE logs them)"
}

// tracef prints only at TRACE. Use it for lines whose whole point is
// content or identifiers (message text, name lookups, LID resolution).
func tracef(format string, args ...interface{}) {
	if traceLogging {
		fmt.Printf("[TRACE] "+format, args...)
	}
}

// redactSalt is random per process: tags are stable within one run and
// cannot be matched against a list of hashed phone numbers.
var redactSalt = func() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return b
}()

// redact returns v unchanged at TRACE. Otherwise it returns a short tag
// such as "#3fa2c1@g.us": the "@server" part of a JID is kept because it
// tells phone, LID and group JIDs apart, which most JID bugs depend on.
func redact(v interface{}) string {
	s := fmt.Sprint(v)
	if traceLogging || s == "" {
		return s
	}
	user, server, isJID := strings.Cut(s, "@")
	h := sha256.New()
	h.Write(redactSalt)
	h.Write([]byte(user))
	tag := "#" + hex.EncodeToString(h.Sum(nil)[:3])
	if isJID {
		return tag + "@" + server
	}
	return tag
}

// redactText returns s at TRACE and only its length otherwise.
func redactText(s string) string {
	if traceLogging {
		return s
	}
	return fmt.Sprintf("<%d chars>", utf8.RuneCountInString(s))
}

// redactPath keeps only the file name of a media path at non-TRACE levels:
// media lives under store/<chat JID>/, so the directory names the chat.
func redactPath(p string) string {
	if traceLogging {
		return p
	}
	return filepath.Base(p)
}

// messageLogSummary is the default log line for a live message: direction,
// ID and kind, but not the chat, the sender or the text.
func messageLogSummary(isFromMe bool, id, mediaType string) string {
	what := "message received"
	if isFromMe {
		what = "message sent"
	}
	kind := mediaType
	if kind == "" {
		kind = "text"
	}
	return fmt.Sprintf("%s: id=%s type=%s", what, id, kind)
}
