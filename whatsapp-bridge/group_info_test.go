package main

import "testing"

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
