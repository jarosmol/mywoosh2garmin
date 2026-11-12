package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// App config (persisted to ~/.mywhoosh2garmin/config.json)
// ---------------------------------------------------------------------------

type appConfig struct {
	MyWhooshEmail string `json:"mywhoosh_email"`
	GarminEmail   string `json:"garmin_email"`

	// Device is the key of the Garmin device the rides are labelled as
	// (see devicePresets, or "custom"). Empty means the default.
	Device string `json:"device,omitempty"`
	// DeviceProduct is the Garmin product id when Device is "custom".
	DeviceProduct uint16 `json:"device_product,omitempty"`
	// DeviceSerial is the Unit ID of the user's own Garmin device.
	// When set, Garmin Connect attributes the rides to that device, which
	// lets it post-process them (e.g. cycling VO2max on an Edge).
	DeviceSerial uint32 `json:"device_serial,omitempty"`
	// DeviceSoftwareVersion is the device firmware ×100 (2618 = 26.18);
	// 0 keeps the value from the MyWhoosh file.
	DeviceSoftwareVersion uint16 `json:"device_software_version,omitempty"`
	// GeneratedSerial is a random serial number created on first use, used
	// when DeviceSerial is not set.
	GeneratedSerial uint32 `json:"generated_serial,omitempty"`

	// SyncDays is how many days back to look for rides (default 10).
	SyncDays int `json:"sync_days,omitempty"`

	// SkipOverlapCheck disables the check that skips rides Garmin Connect
	// already has from the same time (e.g. also recorded on a watch).
	SkipOverlapCheck bool `json:"skip_overlap_check,omitempty"`

	// RememberMyWhooshPassword stores the MyWhoosh password encrypted with
	// the current Windows account (DPAPI), so background sync can log in
	// again by itself when the MyWhoosh session expires.
	RememberMyWhooshPassword bool   `json:"remember_mywhoosh_password,omitempty"`
	MyWhooshPasswordEnc      string `json:"mywhoosh_password_enc,omitempty"`
}

func (c appConfig) syncDays() int {
	if c.SyncDays <= 0 {
		return 10
	}
	return c.SyncDays
}

var configMu sync.Mutex

func appConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mywhoosh2garmin")
}

func loadAppConfig() appConfig {
	configMu.Lock()
	defer configMu.Unlock()
	var cfg appConfig
	data, err := os.ReadFile(filepath.Join(appConfigDir(), "config.json"))
	if err == nil {
		json.Unmarshal(data, &cfg)
	}
	return cfg
}

func saveAppConfig(cfg appConfig) {
	configMu.Lock()
	defer configMu.Unlock()
	dir := appConfigDir()
	os.MkdirAll(dir, 0o700)
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600)
}

// ---------------------------------------------------------------------------
// Synced-activity tracker (persisted to ~/.mywhoosh2garmin/synced.json)
// ---------------------------------------------------------------------------

type syncedTracker struct {
	mu       sync.Mutex
	path     string
	uploaded map[string]string // activityID → upload timestamp / note
}

func newSyncedTracker(dir string) *syncedTracker {
	st := &syncedTracker{
		path:     filepath.Join(dir, "synced.json"),
		uploaded: make(map[string]string),
	}
	data, err := os.ReadFile(st.path)
	if err == nil {
		json.Unmarshal(data, &st.uploaded)
	}
	return st
}

func (st *syncedTracker) IsSynced(activityID string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	_, ok := st.uploaded[activityID]
	return ok
}

func (st *syncedTracker) MarkSynced(activityID string) {
	st.mark(activityID, time.Now().Format(time.RFC3339))
}

// MarkSkipped records an activity that was deliberately not uploaded
// (e.g. Garmin already has it from a watch) so it is not retried.
func (st *syncedTracker) MarkSkipped(activityID, reason string) {
	st.mark(activityID, "skipped "+time.Now().Format(time.RFC3339)+": "+reason)
}

func (st *syncedTracker) mark(activityID, value string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.uploaded[activityID] = value
	data, _ := json.MarshalIndent(st.uploaded, "", "  ")
	os.WriteFile(st.path, data, 0o600)
}
