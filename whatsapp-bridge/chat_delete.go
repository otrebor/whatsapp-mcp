package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// registerChatDeleteEndpoint wires POST /api/chat-delete onto an existing mux.
// It deletes a chat "for me": an app-state patch removes it from all of the
// user's own devices (phone included); the other party keeps their copy. The
// chat and its messages are then removed from messages.db as well.
func registerChatDeleteEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, sessions *sessionHolder, messageStore *MessageStore) {
	mux.HandleFunc("/api/chat-delete", auth(func(w http.ResponseWriter, r *http.Request) {
		client := sessions.current()
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeResp := func(status int, success bool, msg string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(SendMessageResponse{Success: success, Message: msg})
		}

		var req struct {
			ChatJID string `json:"chat_jid"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeResp(http.StatusBadRequest, false, "Invalid request format")
			return
		}
		raw := strings.TrimPrefix(strings.TrimSpace(req.ChatJID), "+")
		if raw != "" && !strings.Contains(raw, "@") {
			raw += "@" + types.DefaultUserServer
		}
		chatJID, err := types.ParseJID(raw)
		if raw == "" || err != nil || chatJID.User == "" {
			writeResp(http.StatusBadRequest, false, "chat_jid must be a phone number or chat JID")
			return
		}
		if !whatsappReady(client) {
			writeResp(http.StatusServiceUnavailable, false, "Not connected to WhatsApp")
			return
		}

		// The patch names the chat's latest message so WhatsApp knows which
		// range to delete. Chats whose history never synced have no stored
		// messages; fall back to the chat's last_message_time with no key.
		var (
			msgID  string
			fromMe bool
			rawTS  any
			sender string
			key    *waCommon.MessageKey
		)
		err = messageStore.db.QueryRow(
			`SELECT id, is_from_me, timestamp, sender FROM messages
			  WHERE chat_jid = ? ORDER BY timestamp DESC LIMIT 1`,
			chatJID.String(),
		).Scan(&msgID, &fromMe, &rawTS, &sender)
		if err == nil {
			key = &waCommon.MessageKey{
				RemoteJID: proto.String(chatJID.String()),
				FromMe:    proto.Bool(fromMe),
				ID:        proto.String(msgID),
			}
			if chatJID.Server == types.GroupServer && !fromMe && sender != "" {
				key.Participant = proto.String(sender + "@" + types.DefaultUserServer)
			}
		} else if err = messageStore.db.QueryRow(
			`SELECT last_message_time FROM chats WHERE jid = ?`,
			chatJID.String(),
		).Scan(&rawTS); err != nil || rawTS == nil {
			writeResp(http.StatusNotFound, false, fmt.Sprintf("No stored messages or chat for %s; cannot build delete request", chatJID))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		patch := appstate.BuildDeleteChat(chatJID, anchorTime(rawTS), key, true)
		if err := client.SendAppState(ctx, patch); err != nil {
			writeResp(http.StatusBadGateway, false, fmt.Sprintf("Failed to delete chat: %v", err))
			return
		}

		var removed int64
		if res, dbErr := messageStore.db.Exec(`DELETE FROM messages WHERE chat_jid = ?`, chatJID.String()); dbErr == nil {
			removed, _ = res.RowsAffected()
		}
		_, _ = messageStore.db.Exec(`DELETE FROM chats WHERE jid = ?`, chatJID.String())

		fmt.Printf("← /api/chat-delete chat=%s local_messages_removed=%d\n", chatJID, removed)
		writeResp(http.StatusOK, true, fmt.Sprintf("Chat %s deleted for you (%d local messages removed)", chatJID, removed))
	}))
}
