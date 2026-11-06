package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mywhoosh2garmin/mywhoosh"
)

const (
	logFileName    = "sync.log"
	maxLogSize     = 1 << 20 // 1 MB, then rotated to sync.log.old
	lockFileName   = "sync.lock"
	staleLockAfter = 30 * time.Minute
	noticeFileName = "last_notice"
	noticeEvery    = 24 * time.Hour
)

// runHeadless performs one unattended sync (used by the scheduled task).
// Output goes to ~/.mywhoosh2garmin/sync.log. Returns the process exit code.
func runHeadless() int {
	dir := appConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	logger, closeLog := openSyncLog(dir)
	defer closeLog()
	write := func(msg string) {
		for _, line := range strings.Split(strings.TrimRight(msg, "\n"), "\n") {
			logger.Println(line)
		}
	}
	logFn = func(format string, args ...interface{}) { write(fmt.Sprintf(format, args...)) }
	mywhoosh.SetDebugFn(func(format string, args ...interface{}) { write(fmt.Sprintf(format, args...)) })

	unlock, err := acquireLock(dir)
	if err != nil {
		write("Another sync is already running — skipping this run.")
		return 0
	}
	defer unlock()

	write("=== Background sync started ===")
	cfg := loadAppConfig()
	s := newSyncer(dir, write)

	acts, err := s.recentActivities(credentials{}, cfg.syncDays())
	if err == nil {
		var sum syncSummary
		sum, err = s.syncPending(acts, credentials{})
		if err == nil || sum.Found > 0 {
			write(fmt.Sprintf("Rides in the last %d days: %d — %s", cfg.syncDays(), sum.Found, sum))
		}
	}

	if err != nil {
		write("❌ " + err.Error())
		if errors.Is(err, errNeedsLogin) {
			notifyOnce(dir, "MyWhoosh2Garmin",
				"Automatic sync needs you to log in again.\n\n"+
					"Open MyWhoosh2Garmin, enter your password (and Garmin code if asked) "+
					"and click Fetch Activities.\n\nDetails: "+err.Error())
		}
		write("=== Background sync finished with errors ===")
		return 1
	}

	write("=== Background sync finished ===")
	return 0
}

// openSyncLog opens (and rotates when large) the background sync log.
func openSyncLog(dir string) (*log.Logger, func()) {
	path := filepath.Join(dir, logFileName)
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogSize {
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return log.New(os.Stderr, "", log.LstdFlags), func() {}
	}
	return log.New(f, "", log.LstdFlags), func() { f.Close() }
}

// acquireLock prevents two syncs (e.g. scheduled task + app window) from
// uploading the same ride at the same time. Old locks left by a crash expire.
func acquireLock(dir string) (func(), error) {
	path := filepath.Join(dir, lockFileName)
	if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) > staleLockAfter {
		os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	f.Close()
	return func() { os.Remove(path) }, nil
}

// notifyOnce shows a message box at most once per noticeEvery, so an expired
// login doesn't pop up a window every hour.
func notifyOnce(dir, title, message string) {
	path := filepath.Join(dir, noticeFileName)
	if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) < noticeEvery {
		return
	}
	os.WriteFile(path, []byte(time.Now().Format(time.RFC3339)), 0o600)
	showNotice(title, message)
}

// clearNotice resets the notice timer after a successful interactive login.
func clearNotice(dir string) {
	os.Remove(filepath.Join(dir, noticeFileName))
}
