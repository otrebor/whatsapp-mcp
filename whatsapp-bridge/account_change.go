package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// wipeOnAccountChange deletes the previous account's local history (chats,
// messages, calls and downloaded media) when a different WhatsApp account
// links. Re-linking the same account always keeps history: WhatsApp's history
// sync re-sends only recent months, so the local copy can't be rebuilt.
var wipeOnAccountChange = getEnvBool("WHATSAPP_WIPE_ON_ACCOUNT_CHANGE", true)

// bridge_meta keys.
const (
	metaOwnerPhone     = "owner_phone"
	metaOwnerLID       = "owner_lid"
	metaHistoryWipedAt = "history_wiped_at"
)

// accountOwner identifies the WhatsApp account messages.db belongs to, by the
// user part of its phone JID and LID (no device suffix).
type accountOwner struct {
	Phone string
	LID   string
}

func ownerFromJIDs(id, lid types.JID) accountOwner {
	return accountOwner{Phone: id.User, LID: lid.User}
}

// sameAccount compares by LID when both sides know it, because WhatsApp's
// "Change number" keeps the account (and, as far as we know, its LID) under a
// new phone number; wiping on that would destroy the user's own history.
// Without both LIDs it falls back to the phone number.
func sameAccount(prev, next accountOwner) bool {
	if prev.LID != "" && next.LID != "" {
		return prev.LID == next.LID
	}
	return prev.Phone == next.Phone
}

// bridge_meta holds bridge-wide facts about messages.db, such as which
// account it belongs to.
func ensureMetaTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS bridge_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	return err
}

// metaGet returns "" for a missing key. ensureMessageStoreSchema creates the
// table at startup.
func (store *MessageStore) metaGet(key string) (string, error) {
	var value string
	err := store.db.QueryRow(`SELECT value FROM bridge_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (store *MessageStore) metaSet(key, value string) error {
	_, err := store.db.Exec(
		`INSERT INTO bridge_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

func (store *MessageStore) loadOwner() (accountOwner, error) {
	phone, err := store.metaGet(metaOwnerPhone)
	if err != nil {
		return accountOwner{}, err
	}
	lid, err := store.metaGet(metaOwnerLID)
	return accountOwner{Phone: phone, LID: lid}, err
}

// saveOwner records next, keeping a known LID when next doesn't carry one.
func (store *MessageStore) saveOwner(next accountOwner) error {
	if err := store.metaSet(metaOwnerPhone, next.Phone); err != nil {
		return err
	}
	if next.LID == "" {
		return nil
	}
	return store.metaSet(metaOwnerLID, next.LID)
}

// recordOwnerIfMissing remembers the account of an already-paired session.
// Installs from before owner tracking have history but no record; recording
// it here (instead of on the next pairing) means their first re-link after an
// upgrade can't be mistaken for a different account.
func (store *MessageStore) recordOwnerIfMissing(owner accountOwner) error {
	prev, err := store.loadOwner()
	if err != nil || prev.Phone != "" || owner.Phone == "" {
		return err
	}
	return store.saveOwner(owner)
}

// historyWipedAt reports when history was last wiped for an account change.
func (store *MessageStore) historyWipedAt() (time.Time, bool) {
	value, err := store.metaGet(metaHistoryWipedAt)
	if err != nil || value == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, value)
	return t, err == nil
}

// onAccountLinked runs on events.PairSuccess, synchronously, so it finishes
// before the new account's history sync starts writing. It wipes the previous
// account's history when a different account linked (and wiping is enabled),
// then records next as the owner. With no recorded owner nothing is wiped.
func (store *MessageStore) onAccountLinked(next accountOwner, wipe bool, storeDir string, logger waLog.Logger) (wiped bool, err error) {
	prev, err := store.loadOwner()
	if err != nil {
		return false, fmt.Errorf("load account owner: %w", err)
	}
	if prev.Phone != "" && !sameAccount(prev, next) {
		if wipe {
			logger.Warnf("A different WhatsApp account linked; deleting the previous account's local history (WHATSAPP_WIPE_ON_ACCOUNT_CHANGE=false keeps it)")
			if err := store.wipeHistory(storeDir); err != nil {
				return false, fmt.Errorf("wipe previous account's history: %w", err)
			}
			wiped = true
			if err := store.metaSet(metaHistoryWipedAt, time.Now().UTC().Format(time.RFC3339)); err != nil {
				logger.Warnf("Failed to record history wipe time: %v", err)
			}
		} else {
			logger.Warnf("A different WhatsApp account linked; keeping the previous account's history (WHATSAPP_WIPE_ON_ACCOUNT_CHANGE=false), so both accounts' chats are listed together")
		}
	}
	if err := store.saveOwner(next); err != nil {
		return wiped, fmt.Errorf("record account owner: %w", err)
	}
	return wiped, nil
}

// wipeHistory empties the history tables in one transaction (the MCP server
// may be reading the same file, so rows are deleted rather than the file) and
// removes downloaded media: the per-chat directories under storeDir, which
// are named after chat JIDs and so always contain "@". Session files
// (whatsapp.db, messages.db, .bridge-token) are never touched.
func (store *MessageStore) wipeHistory(storeDir string) error {
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	for _, table := range []string{"messages", "calls", "chats"} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		return fmt.Errorf("list media directories: %w", err)
	}
	var firstErr error
	for _, entry := range entries {
		if !entry.IsDir() || !strings.Contains(entry.Name(), "@") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(storeDir, entry.Name())); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("remove media for %s: %w", entry.Name(), err)
		}
	}
	return firstErr
}
