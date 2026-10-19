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

// GroupCreateRequest is the body accepted by POST /api/group-create.
type GroupCreateRequest struct {
	Name         string   `json:"name"`
	Participants []string `json:"participants"` // phone numbers (no +) or JIDs
}

// parseParticipantJIDs turns phone numbers (optionally with +) or user JIDs
// into JIDs, rejecting group or other non-user JIDs.
func parseParticipantJIDs(raw []string) ([]types.JID, error) {
	members := make([]types.JID, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimPrefix(strings.TrimSpace(p), "+")
		jid := types.JID{User: p, Server: types.DefaultUserServer}
		if strings.Contains(p, "@") {
			parsed, err := types.ParseJID(p)
			if err != nil {
				return nil, fmt.Errorf("invalid participant %q: %v", p, err)
			}
			jid = parsed
		}
		if jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) {
			return nil, fmt.Errorf("participant %q must be a user phone number or JID", p)
		}
		members = append(members, jid)
	}
	return members, nil
}

// participantFailures lists members WhatsApp refused (e.g. privacy settings),
// which come back with a non-zero error code.
func participantFailures(participants []types.GroupParticipant) []string {
	var failed []string
	for _, p := range participants {
		if p.Error != 0 {
			user := p.JID.User
			if !p.PhoneNumber.IsEmpty() {
				user = p.PhoneNumber.User
			}
			failed = append(failed, fmt.Sprintf("%s (error %d)", user, p.Error))
		}
	}
	return failed
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
		if !whatsappReady(client) {
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

	// Adds members to an existing group (caller must be admin).
	mux.HandleFunc("/api/group-participants-add", auth(func(w http.ResponseWriter, r *http.Request) {
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
			JID          string   `json:"jid"`
			Participants []string `json:"participants"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeResp(http.StatusBadRequest, map[string]interface{}{"success": false, "message": "Invalid request format"})
			return
		}
		if !strings.Contains(req.JID, "@") && req.JID != "" {
			req.JID += "@g.us"
		}
		jid, err := types.ParseJID(req.JID)
		if req.JID == "" || err != nil || jid.Server != types.GroupServer {
			writeResp(http.StatusBadRequest, map[string]interface{}{"success": false, "message": "jid must be a group JID (…@g.us)"})
			return
		}
		if len(req.Participants) == 0 {
			writeResp(http.StatusBadRequest, map[string]interface{}{"success": false, "message": "at least one participant is required"})
			return
		}
		members, err := parseParticipantJIDs(req.Participants)
		if err != nil {
			writeResp(http.StatusBadRequest, map[string]interface{}{"success": false, "message": err.Error()})
			return
		}
		if !whatsappReady(client) {
			writeResp(http.StatusServiceUnavailable, map[string]interface{}{"success": false, "message": "Not connected to WhatsApp"})
			return
		}

		fmt.Printf("→ /api/group-participants-add jid=%s participants=%d\n", jid, len(members))
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		result, err := client.UpdateGroupParticipants(ctx, jid, members, whatsmeow.ParticipantChangeAdd)
		if err != nil {
			writeResp(http.StatusBadGateway, map[string]interface{}{"success": false, "message": fmt.Sprintf("Failed to add participants: %v", err)})
			return
		}
		failed := participantFailures(result)
		msg := fmt.Sprintf("Added %d of %d participants", len(result)-len(failed), len(result))
		if len(failed) > 0 {
			msg += "; could not add: " + strings.Join(failed, ", ")
		}
		fmt.Printf("← /api/group-participants-add jid=%s added=%d failed=%d\n", jid, len(result)-len(failed), len(failed))
		writeResp(http.StatusOK, map[string]interface{}{
			"success": true,
			"message": msg,
			"added":   len(result) - len(failed),
			"failed":  failed,
		})
	}))

	// Sets (or clears, with an empty topic) the group description.
	mux.HandleFunc("/api/group-topic", auth(func(w http.ResponseWriter, r *http.Request) {
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
			JID   string `json:"jid"`
			Topic string `json:"topic"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeResp(http.StatusBadRequest, false, "Invalid request format")
			return
		}
		if !strings.Contains(req.JID, "@") && req.JID != "" {
			req.JID += "@g.us"
		}
		jid, err := types.ParseJID(req.JID)
		if req.JID == "" || err != nil || jid.Server != types.GroupServer {
			writeResp(http.StatusBadRequest, false, "jid must be a group JID (…@g.us)")
			return
		}
		// WhatsApp caps group descriptions at 2048 characters.
		if len([]rune(req.Topic)) > 2048 {
			writeResp(http.StatusBadRequest, false, "topic must be at most 2048 characters")
			return
		}
		if !whatsappReady(client) {
			writeResp(http.StatusServiceUnavailable, false, "Not connected to WhatsApp")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := client.SetGroupTopic(ctx, jid, "", "", req.Topic); err != nil {
			writeResp(http.StatusBadGateway, false, fmt.Sprintf("Failed to set group description: %v", err))
			return
		}
		fmt.Printf("← /api/group-topic jid=%s topic_len=%d\n", jid, len(req.Topic))
		writeResp(http.StatusOK, true, "Group description updated")
	}))

	mux.HandleFunc("/api/group-create", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		writeErr := func(status int, msg string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(GroupInfoResponse{Success: false, Message: msg})
		}

		var req GroupCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(http.StatusBadRequest, "Invalid request format")
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" {
			writeErr(http.StatusBadRequest, "name is required")
			return
		}
		// WhatsApp rejects longer subjects.
		if len([]rune(req.Name)) > 100 {
			writeErr(http.StatusBadRequest, "name must be at most 100 characters")
			return
		}
		if len(req.Participants) == 0 {
			writeErr(http.StatusBadRequest, "at least one participant is required")
			return
		}

		members, err := parseParticipantJIDs(req.Participants)
		if err != nil {
			writeErr(http.StatusBadRequest, err.Error())
			return
		}

		if !whatsappReady(client) {
			writeErr(http.StatusServiceUnavailable, "Not connected to WhatsApp")
			return
		}

		fmt.Printf("→ /api/group-create name_len=%d participants=%d\n", len(req.Name), len(members))
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		info, err := client.CreateGroup(ctx, whatsmeow.ReqCreateGroup{Name: req.Name, Participants: members})
		if err != nil {
			writeErr(http.StatusBadGateway, fmt.Sprintf("Failed to create group: %v", err))
			return
		}

		resp := groupInfoResponse(ctx, client, info)
		failed := participantFailures(info.Participants)
		if len(failed) > 0 {
			resp.Message = "Group created, but some participants could not be added: " + strings.Join(failed, ", ")
		}
		fmt.Printf("← /api/group-create jid=%s participants=%d failed=%d\n", info.JID, len(info.Participants), len(failed))
		_ = json.NewEncoder(w).Encode(resp)
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
		if !whatsappReady(client) {
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
