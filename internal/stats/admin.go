package stats

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"glocker/internal/store"

	"gorm.io/gorm"
)

// adminUserView is a user row for the admin panel. deviceLimit is the effective
// cap (-1 = unlimited); self marks the admin's own row (which can't be deleted).
type adminUserView struct {
	ID          uint   `json:"id"`
	Email       string `json:"email"`
	Verified    bool   `json:"verified"`
	DeviceLimit int    `json:"deviceLimit"`
	Devices     int    `json:"devices"`
	Records     int64  `json:"records"`    // synced data rows (violations/unblocks/lifecycle/usage/heartbeat)
	LastSyncAt  *int64 `json:"lastSyncAt"` // epoch ms of the last ingest, or null
	CreatedAt   int64  `json:"createdAt"`
	IsAdmin     bool   `json:"isAdmin"`
	Self        bool   `json:"self"`
	SubStatus   string `json:"subStatus"` // "" free, "active", "canceled", "expired"
}

// recordCount sums the actual synced data sources (not rules/ignored config).
func recordCount(c map[string]int64) int64 {
	return c["violations"] + c["unblocks"] + c["lifecycle"] + c["usage"] + c["heartbeat"]
}

// handleAdminUsers is the admin-gated user-management endpoint:
//
//	GET             list all accounts
//	DELETE ?id=N    delete an account and all its data (not your own)
//	PUT    ?id=N    set an account's device limit ({"deviceLimit": n})
//	PATCH  ?id=N    clear an account's subscription (reset to the free tier)
func handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	me := userFrom(r)
	switch r.Method {
	case http.MethodGet:
		adminListUsers(w, me)
	case http.MethodDelete:
		adminDeleteUser(w, r, me)
	case http.MethodPut:
		adminSetDeviceLimit(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func adminListUsers(w http.ResponseWriter, me *store.User) {
	users, err := db.ListUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]adminUserView, 0, len(users))
	var totalRecords int64
	for i := range users {
		u := users[i]
		devices, _ := db.CountAPITokens(u.ID)
		records := recordCount(db.Counts(u.ID))
		totalRecords += records
		var lastSync *int64
		if st, ok, _ := db.SyncStatusFor(u.ID); ok && !st.LastIngestAt.IsZero() {
			ms := st.LastIngestAt.UnixMilli()
			lastSync = &ms
		}
		out = append(out, adminUserView{
			ID: u.ID, Email: u.Email, Verified: u.Verified,
			DeviceLimit: store.EffectiveDeviceLimit(&u), Devices: devices,
			Records: records, LastSyncAt: lastSync,
			CreatedAt: u.CreatedAt.UnixMilli(), IsAdmin: isAdmin(&u), Self: u.ID == me.ID,
			SubStatus: u.SubStatus,
		})
	}
	writeJSON(w, map[string]any{
		"users":  out,
		"totals": map[string]any{"accounts": len(out), "records": totalRecords},
	})
}

func adminDeleteUser(w http.ResponseWriter, r *http.Request, me *store.User) {
	id, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	if uint(id) == me.ID {
		http.Error(w, "refusing to delete your own admin account", http.StatusBadRequest)
		return
	}
	if err := db.DeleteUserData(uint(id)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// adminCancelSubscription removes an account's subscription and returns it to the
// free tier — one admin action covering the whole job, so an operator never needs
// the Polar merchant dashboard. If the account has a live provider subscription it
// is revoked at Polar first (so nothing is left billing), then glockpeek's record
// is reset to free. An already-canceled or already-gone subscription is treated as
// success, so this doubles as the drift-repair path too.
func adminCancelSubscription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	u, err := db.UserByID(uint(id))
	if err != nil {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	// Revoke the live provider subscription first so we don't orphan it at Polar.
	if billingEnabled && u.SubID != "" {
		if _, err := revokePolarSubscription(r.Context(), u.SubID); err != nil {
			log.Printf("billing: admin revoke failed for user %d: %v", u.ID, err)
			http.Error(w, "could not revoke subscription at Polar", http.StatusBadGateway)
			return
		}
	}
	// Reset to the free tier (blank status, free cap, no stored sub id). The
	// high-water stamp inside ClearSubscription drops the revoke's own late webhook.
	if err := db.ClearSubscription(u.ID); err != nil {
		log.Printf("billing: recording admin cancel for user %d: %v", u.ID, err)
		http.Error(w, "revoked at Polar but could not reset the record here", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// adminGrantSubscription comps an account the paid tier without a payment — the
// operator grants access directly (a friend, a beta tester, a manual make-good).
// It writes an "active" subscription with the paid device cap and, deliberately,
// an EMPTY provider sub id: the on-load reconcile skips accounts with no SubID, so
// a comp is never fetched from Polar and never clobbered. Optional {"untilMillis":N}
// time-boxes access (HasAccess lapses after SubCurrentEnd); absent/0 = permanent.
// Refuses an account that already has a provider subscription — cancel it first —
// so a comp can't silently orphan a live Polar sub by blanking its id.
func adminGrantSubscription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !billingEnabled {
		http.Error(w, "billing is disabled — every account already has access", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	u, err := db.UserByID(uint(id))
	if err != nil {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	if u.SubID != "" {
		http.Error(w, "account has a provider subscription — cancel it first, then grant", http.StatusConflict)
		return
	}
	var in struct {
		UntilMillis int64 `json:"untilMillis"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
	}
	var end time.Time // zero = permanent
	if in.UntilMillis > 0 {
		end = time.UnixMilli(in.UntilMillis)
	}
	// Empty sub id on purpose (keeps the comp off the Polar reconcile path).
	if err := db.SetSubscription(u.ID, store.SubStatusActive, "", end, billing.PaidDeviceLimit, time.Now()); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "no such user", http.StatusNotFound)
			return
		}
		log.Printf("billing: admin grant failed for user %d: %v", u.ID, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "subStatus": store.SubStatusActive})
}

func adminSetDeviceLimit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	var in struct {
		DeviceLimit int `json:"deviceLimit"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if err := db.SetDeviceLimitByID(uint(id), in.DeviceLimit); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "no such user", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
