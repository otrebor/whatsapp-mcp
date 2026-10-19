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

// GroupParticipantInfo is one member of a group as returned by /api/group-info.
type GroupParticipantInfo struct {
	JID          string `json:"jid"`
	PhoneNumber  string `json:"phone_number,omitempty"`
	LID          string `json:"lid,omitempty"`
	Name         string `json:"name,omitempty"`
	IsAdmin      bool   `json:"is_admin"`
	IsSuperAdmin bool   `json:"is_super_admin"`
}

// GroupInfoResponse is the body returned by GET /api/group-info.
type GroupInfoResponse struct {
	Success      bool                   `json:"success"`
	Message      string                 `json:"message,omitempty"`
	JID          string                 `json:"jid,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Topic        string                 `json:"topic,omitempty"`
	Participants []GroupParticipantInfo `json:"participants,omitempty"`
}

// contactName picks the most human-friendly name whatsmeow knows for a JID.
func contactName(c types.ContactInfo) string {
	for _, n := range []string{c.FullName, c.FirstName, c.PushName, c.BusinessName} {
		if n != "" {
			return n
		}
	}
	return ""
}

// groupInfoResponse converts whatsmeow group info into the API shape,
// resolving member names from the contact store.
func groupInfoResponse(ctx context.Context, client *whatsmeow.Client, info *types.GroupInfo) GroupInfoResponse {
	participants := make([]GroupParticipantInfo, 0, len(info.Participants))
	for _, p := range info.Participants {
		pi := GroupParticipantInfo{
			JID:          p.JID.String(),
			IsAdmin:      p.IsAdmin,
			IsSuperAdmin: p.IsSuperAdmin,
		}
		if !p.PhoneNumber.IsEmpty() {
			pi.PhoneNumber = p.PhoneNumber.User
		}
		if !p.LID.IsEmpty() {
			pi.LID = p.LID.User
		}
		// Contacts may be keyed by either phone or LID; try both.
		for _, cj := range []types.JID{p.PhoneNumber, p.LID, p.JID} {
			if cj.IsEmpty() {
				continue
			}
			if c, cErr := client.Store.Contacts.GetContact(ctx, cj); cErr == nil && c.Found {
				if pi.Name = contactName(c); pi.Name != "" {
					break
				}
			}
		}
		if pi.Name == "" {
			pi.Name = p.DisplayName
		}
		participants = append(participants, pi)
	}
	return GroupInfoResponse{
		Success:      true,
		JID:          info.JID.String(),
		Name:         info.Name,
		Topic:        info.Topic,
		Participants: participants,
	}
}

// registerGroupInfoEndpoint wires the /api/group-* endpoints onto an existing
// mux. group-info asks WhatsApp for the current member list, which is the only
// reliable source (messages.db only knows members who posted).
func registerGroupInfoEndpoint(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc, client *whatsmeow.Client) {
	// Returns the existing invite link; never resets it. Only admins can fetch it.
	mux.HandleFunc("/api/group-invite-link", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeResp := func(status int, resp map[string]interface{}) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(resp)
		}

		raw := r.URL.Query().Get("jid")
		if !strings.Contains(raw, "@") && raw != "" {
			raw += "@g.us"
		}
		jid, err := types.ParseJID(raw)
		if raw == "" || err != nil || jid.Server != types.GroupServer {
			writeResp(http.StatusBadRequest, map[string]interface{}{"success": false, "message": "jid must be a group JID (…@g.us)"})
			return
		}
		if !client.IsConnected() {
			writeResp(http.StatusServiceUnavailable, map[string]interface{}{"success": false, "message": "Not connected to WhatsApp"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		link, err := client.GetGroupInviteLink(ctx, jid, false)
		if err != nil {
			writeResp(http.StatusBadGateway, map[string]interface{}{"success": false, "message": fmt.Sprintf("Failed to get invite link: %v", err)})
			return
		}
		writeResp(http.StatusOK, map[string]interface{}{"success": true, "jid": jid.String(), "link": link})
	}))

	mux.HandleFunc("/api/group-info", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeErr := func(status int, msg string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(GroupInfoResponse{Success: false, Message: msg})
		}

		raw := r.URL.Query().Get("jid")
		if raw == "" {
			writeErr(http.StatusBadRequest, "jid is required")
			return
		}
		if !strings.Contains(raw, "@") {
			raw += "@g.us"
		}
		jid, err := types.ParseJID(raw)
		if err != nil || jid.Server != types.GroupServer {
			writeErr(http.StatusBadRequest, fmt.Sprintf("jid must be a group JID (…@g.us), got %q", raw))
			return
		}
		if !client.IsConnected() {
			writeErr(http.StatusServiceUnavailable, "Not connected to WhatsApp")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		info, err := client.GetGroupInfo(ctx, jid)
		if err != nil {
			writeErr(http.StatusBadGateway, fmt.Sprintf("Failed to get group info: %v", err))
			return
		}

		resp := groupInfoResponse(ctx, client, info)
		fmt.Printf("← /api/group-info jid=%s participants=%d\n", jid, len(resp.Participants))
		_ = json.NewEncoder(w).Encode(resp)
	}))
}
