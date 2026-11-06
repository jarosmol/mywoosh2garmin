//go:build windows

package main

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// ---------------------------------------------------------------------------
// Password encryption with Windows DPAPI.
// The encrypted value can only be decrypted by the same Windows user account
// on the same computer — copying config.json elsewhere does not reveal it.
// ---------------------------------------------------------------------------

const secretStorageSupported = true

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	user32                 = syscall.NewLazyDLL("user32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	procLocalFree          = kernel32.NewProc("LocalFree")
	procMessageBoxW        = user32.NewProc("MessageBoxW")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(d []byte) *dataBlob {
	if len(d) == 0 {
		return &dataBlob{}
	}
	return &dataBlob{cbData: uint32(len(d)), pbData: &d[0]}
}

func (b *dataBlob) bytes() []byte {
	if b.pbData == nil || b.cbData == 0 {
		return nil
	}
	return append([]byte(nil), unsafe.Slice(b.pbData, b.cbData)...)
}

const cryptprotectUIForbidden = 0x1

func protectSecret(plain string) (string, error) {
	data := []byte(plain)
	in := newBlob(data)
	var out dataBlob
	r, _, err := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(in)), 0, 0, 0, 0,
		cryptprotectUIForbidden,
		uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(data)
	if r == 0 {
		return "", fmt.Errorf("CryptProtectData: %w", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return base64.StdEncoding.EncodeToString(out.bytes()), nil
}

func unprotectSecret(enc string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	in := newBlob(data)
	var out dataBlob
	r, _, callErr := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(in)), 0, 0, 0, 0,
		cryptprotectUIForbidden,
		uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(data)
	if r == 0 {
		return "", fmt.Errorf("CryptUnprotectData: %w", callErr)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return string(out.bytes()), nil
}

// ---------------------------------------------------------------------------
// Notification (simple message box; used by background mode only when the
// user has to do something, e.g. log in again).
// ---------------------------------------------------------------------------

func showNotice(title, message string) {
	const (
		mbOK            = 0x00000000
		mbIconWarning   = 0x00000030
		mbSetForeground = 0x00010000
		mbTopmost       = 0x00040000
	)
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(message)
	procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(t)),
		mbOK|mbIconWarning|mbSetForeground|mbTopmost)
}

// ---------------------------------------------------------------------------
// Automatic sync via Windows Task Scheduler (schtasks.exe).
// The task runs as the current user, only while logged in, without a window.
// ---------------------------------------------------------------------------

const scheduleSupported = true

const taskName = "MyWhoosh2Garmin auto-sync"

func schtasks(args ...string) (string, error) {
	cmd := exec.Command("schtasks.exe", args...)
	// Don't flash a console window
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func installSchedule(exePath string, everyMinutes int) error {
	out, err := schtasks("/Create", "/F",
		"/TN", taskName,
		"/SC", "MINUTE",
		"/MO", strconv.Itoa(everyMinutes),
		"/TR", fmt.Sprintf(`"%s" --sync`, exePath))
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

func removeSchedule() error {
	out, err := schtasks("/Delete", "/F", "/TN", taskName)
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

func scheduleInstalled() bool {
	_, err := schtasks("/Query", "/TN", taskName)
	return err == nil
}
