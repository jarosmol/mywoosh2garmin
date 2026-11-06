//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
)

const secretStorageSupported = false

func protectSecret(string) (string, error) {
	return "", errors.New("remembering the password is only supported on Windows")
}

func unprotectSecret(string) (string, error) {
	return "", errors.New("remembering the password is only supported on Windows")
}

func showNotice(title, message string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, message)
}

const scheduleSupported = false

func installSchedule(exePath string, everyMinutes int) error {
	return fmt.Errorf("automatic sync setup is Windows-only; on Linux add a cron entry, e.g.:\n"+
		"  */%d * * * * %q --sync", everyMinutes, exePath)
}

func removeSchedule() error {
	return errors.New("automatic sync setup is Windows-only")
}

func scheduleInstalled() bool { return false }
