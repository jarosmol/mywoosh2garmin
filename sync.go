package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mywhoosh2garmin/garmin"
	"mywhoosh2garmin/mywhoosh"
)

// errNeedsLogin means a session expired and the user has to log in from the
// app window (password and/or MFA code needed). Background mode stops on it.
var errNeedsLogin = errors.New("login required")

// overlapThreshold: a ride is treated as already present in Garmin Connect if
// an existing activity covers at least this fraction of the shorter of the two.
const overlapThreshold = 0.5

// credentials are typed in the app window. In background mode they are empty
// (except a remembered, encrypted MyWhoosh password).
type credentials struct {
	MyWhooshEmail    string
	MyWhooshPassword string
	GarminEmail      string
	GarminPassword   string
	PromptMFA        garmin.MFAPrompt
}

type outcome int

const (
	outcomeUploaded outcome = iota
	outcomeDuplicate
	outcomeOverlap
)

// syncer holds the shared logic used by both the GUI and background mode.
type syncer struct {
	dir     string
	tracker *syncedTracker
	mw      *mywhoosh.Client
	gc      *garmin.Client
	log     func(string)

	authMu sync.Mutex // serialises logins (avoids double MFA prompts)
}

func newSyncer(dir string, log func(string)) *syncer {
	return &syncer{
		dir:     dir,
		tracker: newSyncedTracker(dir),
		mw:      mywhoosh.NewClient(dir),
		log:     log,
	}
}

func (s *syncer) logf(format string, args ...interface{}) {
	s.log(fmt.Sprintf(format, args...))
}

// ---------------------------------------------------------------------------
// MyWhoosh
// ---------------------------------------------------------------------------

// myWhooshPassword returns the typed password or, failing that, the
// remembered encrypted one.
func myWhooshPassword(cfg appConfig, creds credentials) string {
	if creds.MyWhooshPassword != "" {
		return creds.MyWhooshPassword
	}
	if cfg.RememberMyWhooshPassword && cfg.MyWhooshPasswordEnc != "" {
		pw, err := unprotectSecret(cfg.MyWhooshPasswordEnc)
		if err == nil {
			return pw
		}
	}
	return ""
}

func myWhooshEmail(cfg appConfig, creds credentials) string {
	if creds.MyWhooshEmail != "" {
		return creds.MyWhooshEmail
	}
	return cfg.MyWhooshEmail
}

// loginMyWhoosh performs a fresh MyWhoosh login and, if requested, remembers
// the password encrypted for later background logins.
func (s *syncer) loginMyWhoosh(creds credentials) error {
	cfg := loadAppConfig()
	email := myWhooshEmail(cfg, creds)
	password := myWhooshPassword(cfg, creds)
	if email == "" || password == "" {
		return fmt.Errorf("%w: enter MyWhoosh email & password", errNeedsLogin)
	}

	s.log("Logging in to MyWhoosh…")
	if err := s.mw.Login(email, password); err != nil {
		return fmt.Errorf("MyWhoosh login failed: %w", err)
	}
	s.log("✓ Logged in to MyWhoosh")

	// Remember (or forget) the password as the user chose
	cfg = loadAppConfig()
	if cfg.RememberMyWhooshPassword && creds.MyWhooshPassword != "" {
		enc, err := protectSecret(creds.MyWhooshPassword)
		if err != nil {
			s.log("  ⚠ Could not store password: " + err.Error())
		} else {
			cfg.MyWhooshPasswordEnc = enc
			saveAppConfig(cfg)
		}
	} else if !cfg.RememberMyWhooshPassword && cfg.MyWhooshPasswordEnc != "" {
		cfg.MyWhooshPasswordEnc = ""
		saveAppConfig(cfg)
	}
	return nil
}

func (s *syncer) ensureMyWhoosh(creds credentials) error {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	if s.mw.IsAuthenticated() {
		return nil
	}
	if err := s.mw.Resume(); err == nil {
		s.log("MyWhoosh session resumed")
		return nil
	}
	return s.loginMyWhoosh(creds)
}

// recentActivities fetches rides from the last `days` days, newest first.
// If the cached session was rejected it logs in again once.
func (s *syncer) recentActivities(creds credentials, days int) ([]mywhoosh.Activity, error) {
	if err := s.ensureMyWhoosh(creds); err != nil {
		return nil, err
	}

	s.logf("Fetching activities (last %d days)…", days)
	acts, err := s.mw.GetRecentActivities(days)
	if err == nil {
		return acts, nil
	}

	cfg := loadAppConfig()
	if myWhooshPassword(cfg, creds) == "" {
		if isAuthError(err) {
			return nil, fmt.Errorf("%w: MyWhoosh session expired", errNeedsLogin)
		}
		return nil, err
	}

	s.log("Session may be expired, logging in again…")
	s.authMu.Lock()
	loginErr := s.loginMyWhoosh(creds)
	s.authMu.Unlock()
	if loginErr != nil {
		return nil, loginErr
	}
	return s.mw.GetRecentActivities(days)
}

func isAuthError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "HTTP 401") || strings.Contains(msg, "HTTP 403")
}

// ---------------------------------------------------------------------------
// Garmin
// ---------------------------------------------------------------------------

func (s *syncer) ensureGarmin(creds credentials) (*garmin.Client, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	if s.gc != nil && s.gc.OAuth2 != nil {
		return s.gc, nil
	}

	client := garmin.NewClient(s.dir)
	if err := client.Resume(); err == nil {
		s.gc = client
		return client, nil
	}

	email := creds.GarminEmail
	if email == "" {
		email = loadAppConfig().GarminEmail
	}
	if email == "" || creds.GarminPassword == "" {
		return nil, fmt.Errorf("%w: Garmin session expired — enter Garmin email & password", errNeedsLogin)
	}

	s.log("Logging in to Garmin Connect…")
	if err := client.Login(email, creds.GarminPassword, creds.PromptMFA); err != nil {
		if errors.Is(err, garmin.ErrMFARequired) {
			return nil, fmt.Errorf("%w: %v", errNeedsLogin, err)
		}
		return nil, fmt.Errorf("Garmin login failed: %w", err)
	}
	s.log("✓ Logged in to Garmin Connect")
	s.gc = client
	return client, nil
}

// ---------------------------------------------------------------------------
// One activity
// ---------------------------------------------------------------------------

// processActivity downloads, fixes and uploads a single ride.
func (s *syncer) processActivity(act mywhoosh.Activity, creds credentials) (outcome, error) {
	cfg := loadAppConfig()
	dev := deviceFromConfig(cfg)

	// 1. Download FIT from MyWhoosh
	s.logf("⬇ Downloading: %s (%s)…", act.DisplayName(), act.FormattedDate())
	fileID := act.ActivityFileID
	if fileID == "" {
		fileID = act.ID
	}
	fitData, err := s.mw.DownloadFitFile(fileID)
	if err != nil {
		return 0, fmt.Errorf("download failed: %w", err)
	}
	s.logf("  ✓ Downloaded %d bytes", len(fitData))

	// 2. Save to temp files
	ts := time.Now().Format("20060102_150405")
	inputPath := filepath.Join(os.TempDir(), fmt.Sprintf("mw_%s_%s.fit", act.ID, ts))
	outputPath := filepath.Join(os.TempDir(), fmt.Sprintf("mw_%s_%s_fixed.fit", act.ID, ts))
	if err := os.WriteFile(inputPath, fitData, 0o600); err != nil {
		return 0, fmt.Errorf("save temp file failed: %w", err)
	}
	defer os.Remove(inputPath)
	defer os.Remove(outputPath)

	// 3. Fix the FIT file
	s.log("  🔧 Fixing FIT file…")
	info, err := fixFitFile(inputPath, outputPath, dev)
	if err != nil {
		return 0, fmt.Errorf("fix failed: %w", err)
	}

	// 4. Garmin session
	client, err := s.ensureGarmin(creds)
	if err != nil {
		return 0, err
	}

	// 5. Skip if Garmin already has an activity at the same time
	if !cfg.SkipOverlapCheck && !info.Start.IsZero() && !info.End.IsZero() {
		existing, err := client.ActivitiesBetween(info.Start, info.End)
		if err != nil {
			s.log("  ⚠ Could not check Garmin for an existing activity at this time: " + err.Error())
		} else if hit, ok := findOverlap(info, existing); ok {
			s.logf("  ⏭ Skipped — Garmin already has \"%s\" at this time (e.g. from your watch)", hit.Name)
			s.tracker.MarkSkipped(act.ID, fmt.Sprintf("overlaps Garmin activity %d", hit.ID))
			return outcomeOverlap, nil
		}
	}

	// 6. Upload
	s.log("  ⬆ Uploading to Garmin Connect…")
	if err := client.UploadFIT(outputPath); err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			s.log("  ⚠ Already on Garmin (duplicate)")
			s.tracker.MarkSynced(act.ID)
			return outcomeDuplicate, nil
		}
		if strings.Contains(err.Error(), "token refresh") {
			// The long-lived Garmin token is no longer accepted
			s.authMu.Lock()
			s.gc = nil
			s.authMu.Unlock()
			return 0, fmt.Errorf("%w: Garmin session expired (%v)", errNeedsLogin, err)
		}
		return 0, fmt.Errorf("upload failed: %w", err)
	}

	s.tracker.MarkSynced(act.ID)
	s.log("  ✓ Uploaded to Garmin Connect!")
	return outcomeUploaded, nil
}

// findOverlap returns the first existing activity that substantially
// overlaps the ride in time.
func findOverlap(ride rideInfo, existing []garmin.ExistingActivity) (garmin.ExistingActivity, bool) {
	for _, e := range existing {
		if overlapFraction(ride.Start, ride.End, e.Start, e.End) >= overlapThreshold {
			return e, true
		}
	}
	return garmin.ExistingActivity{}, false
}

// overlapFraction returns how much two time ranges overlap, as a fraction of
// the shorter range (0 = no overlap, 1 = the shorter one is fully covered).
func overlapFraction(aStart, aEnd, bStart, bEnd time.Time) float64 {
	start := aStart
	if bStart.After(start) {
		start = bStart
	}
	end := aEnd
	if bEnd.Before(end) {
		end = bEnd
	}
	overlap := end.Sub(start)
	if overlap <= 0 {
		return 0
	}
	shorter := aEnd.Sub(aStart)
	if d := bEnd.Sub(bStart); d < shorter {
		shorter = d
	}
	if shorter <= 0 {
		return 0
	}
	return overlap.Seconds() / shorter.Seconds()
}

// ---------------------------------------------------------------------------
// Everything pending
// ---------------------------------------------------------------------------

type syncSummary struct {
	Found, Uploaded, Duplicates, Skipped, Failed int
}

// syncPending uploads all not-yet-synced rides from the given list,
// oldest first (so Garmin's training status builds up in order).
// It stops early if a login is needed.
func (s *syncer) syncPending(acts []mywhoosh.Activity, creds credentials) (syncSummary, error) {
	var sum syncSummary
	sum.Found = len(acts)

	var pending []mywhoosh.Activity
	for i := len(acts) - 1; i >= 0; i-- { // acts are newest first
		if !s.tracker.IsSynced(acts[i].ID) {
			pending = append(pending, acts[i])
		}
	}

	for i, act := range pending {
		s.logf("\n[%d/%d] %s", i+1, len(pending), act.DisplayName())
		res, err := s.processActivity(act, creds)
		if err != nil {
			s.log("  ❌ " + err.Error())
			sum.Failed++
			if errors.Is(err, errNeedsLogin) {
				return sum, err
			}
			continue
		}
		switch res {
		case outcomeUploaded:
			sum.Uploaded++
		case outcomeDuplicate:
			sum.Duplicates++
		case outcomeOverlap:
			sum.Skipped++
		}
	}
	return sum, nil
}

func (sum syncSummary) String() string {
	return fmt.Sprintf("%d uploaded, %d already on Garmin, %d skipped (recorded elsewhere), %d failed",
		sum.Uploaded, sum.Duplicates, sum.Skipped, sum.Failed)
}
