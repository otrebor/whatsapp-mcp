package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// registerDisappearingTimerEndpoint wires POST /api/disappearing-timer onto an
// existing mux. It turns disappearing messages on or off for a direct chat or
// a group (groups may restrict this to admins). Only WhatsApp's own durations
// are accepted: the official apps ignore other values and groups reject them.
func registerDisappearingTimerEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, sessions *sessionHolder, messageStore *MessageStore) {
	mux.HandleFunc("/api/disappearing-timer", auth(func(w http.ResponseWriter, r *http.Request) {
		client := sessions.current()
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeResp := func(status int, resp map[string]interface{}) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(resp)
		}

		var req struct {
			ChatJID  string `json:"chat_jid"`
			Duration string `json:"duration"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeResp(http.StatusBadRequest, map[string]interface{}{"success": false, "message": "Invalid request format"})
			return
		}
		raw := strings.TrimPrefix(strings.TrimSpace(req.ChatJID), "+")
		if raw != "" && !strings.Contains(raw, "@") {
			raw += "@" + types.DefaultUserServer
		}
		chatJID, err := types.ParseJID(raw)
		if raw == "" || err != nil || chatJID.User == "" {
			writeResp(http.StatusBadRequest, map[string]interface{}{"success": false, "message": "chat_jid must be a phone number, user JID or group JID"})
			return
		}
		switch chatJID.Server {
		case types.DefaultUserServer, types.HiddenUserServer, types.GroupServer:
		default:
			writeResp(http.StatusBadRequest, map[string]interface{}{"success": false, "message": fmt.Sprintf("can't set disappearing messages in a %s chat", chatJID.Server)})
			return
		}
		timer, ok := whatsmeow.ParseDisappearingTimerString(req.Duration)
		if !ok {
			writeResp(http.StatusBadRequest, map[string]interface{}{"success": false, "message": "duration must be one of off, 24h, 7d, 90d"})
			return
		}
		if !whatsappReady(client) {
			writeResp(http.StatusServiceUnavailable, map[string]interface{}{"success": false, "message": "Not connected to WhatsApp"})
			return
		}

		fmt.Printf("→ /api/disappearing-timer chat=%s timer=%s\n", chatJID, timer)
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		settingTS := time.Now()
		if err := client.SetDisappearingTimer(ctx, chatJID, timer, settingTS); err != nil {
			writeResp(http.StatusBadGateway, map[string]interface{}{"success": false, "message": fmt.Sprintf("Failed to set disappearing messages: %v", err)})
			return
		}
		// Record it now so the next send carries the new timer; WhatsApp's
		// echo of the change would otherwise arrive a moment later.
		seconds := uint32(timer.Seconds())
		if err := messageStore.UpdateChatEphemeralSettings(chatJID.String(), seconds, settingTS.Unix()); err != nil {
			fmt.Printf("Failed to store disappearing timer for %s: %v\n", chatJID, err)
		}

		msg := "Disappearing messages turned off"
		if timer > 0 {
			msg = "Disappearing messages set to " + disappearingLabel(timer)
		}
		writeResp(http.StatusOK, map[string]interface{}{
			"success":          true,
			"message":          msg,
			"chat_jid":         chatJID.String(),
			"duration_seconds": seconds,
		})
	}))
}

// disappearingLabel names one of WhatsApp's standard timers.
func disappearingLabel(timer time.Duration) string {
	switch timer {
	case whatsmeow.DisappearingTimer24Hours:
		return "24 hours"
	case whatsmeow.DisappearingTimer7Days:
		return "7 days"
	case whatsmeow.DisappearingTimer90Days:
		return "90 days"
	}
	return timer.String()
}
