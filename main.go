package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"mywhoosh2garmin/mywhoosh"
)

// autoSyncMinutes is how often the scheduled background sync runs.
const autoSyncMinutes = 60

func main() {
	for _, arg := range os.Args[1:] {
		switch strings.ToLower(arg) {
		case "--sync", "-sync", "/sync":
			os.Exit(runHeadless())
		}
	}
	runGUI()
}

// ---------------------------------------------------------------------------
// GUI
// ---------------------------------------------------------------------------

func runGUI() {
	a := app.New()
	w := a.NewWindow("MyWhoosh2Garmin")
	w.Resize(fyne.NewSize(780, 900))

	dir := appConfigDir()
	cfg := loadAppConfig()

	// --- Log panel ---
	logEntry := widget.NewMultiLineEntry()
	logEntry.Wrapping = fyne.TextWrapWord
	logEntry.Disable()
	logScroll := container.NewVScroll(logEntry)
	logScroll.SetMinSize(fyne.NewSize(0, 140))

	var logMu sync.Mutex
	var logText string

	appendLog := func(msg string) {
		logMu.Lock()
		logText += msg + "\n"
		text := logText
		logMu.Unlock()
		fyne.Do(func() {
			logEntry.Enable()
			logEntry.SetText(text)
			logEntry.CursorRow = len(strings.Split(text, "\n"))
			logEntry.Disable()
			logScroll.ScrollToBottom()
		})
	}

	// Wire FIT processing and MyWhoosh debug logs into the GUI
	logFn = func(format string, args ...interface{}) {
		appendLog(strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
	}
	mywhoosh.SetDebugFn(func(format string, args ...interface{}) {
		appendLog(fmt.Sprintf(format, args...))
	})

	s := newSyncer(dir, appendLog)

	// --- MyWhoosh credentials ---
	mwEmailEntry := widget.NewEntry()
	mwEmailEntry.SetPlaceHolder("MyWhoosh email")
	mwEmailEntry.SetText(cfg.MyWhooshEmail)

	mwPasswordEntry := widget.NewPasswordEntry()
	mwPasswordEntry.SetPlaceHolder("MyWhoosh password")

	// --- Garmin credentials ---
	garminEmailEntry := widget.NewEntry()
	garminEmailEntry.SetPlaceHolder("Garmin email")
	garminEmailEntry.SetText(cfg.GarminEmail)

	garminPasswordEntry := widget.NewPasswordEntry()
	garminPasswordEntry.SetPlaceHolder("Garmin password (only needed first time)")

	mwStatusLabel := widget.NewLabel("")
	garminStatusLabel := widget.NewLabel("")

	// --- Device settings ---
	// Dropdown options: the presets, plus the imported model if it is not listed.
	deviceOptions := func(c appConfig) []string {
		opts := presetLabels()
		if c.Device == customDeviceKey && c.DeviceProduct != 0 {
			opts = append(opts, customLabel(c.DeviceProduct))
		}
		return opts
	}
	deviceSelect := widget.NewSelect(deviceOptions(cfg), nil)
	deviceSelect.SetSelected(deviceFromConfig(cfg).Name)

	unitIDEntry := widget.NewEntry()
	unitIDEntry.SetPlaceHolder("Unit ID of your Garmin (optional)")
	if cfg.DeviceSerial != 0 {
		unitIDEntry.SetText(strconv.FormatUint(uint64(cfg.DeviceSerial), 10))
	}

	deviceInfoLabel := widget.NewLabel("")
	deviceInfoLabel.Wrapping = fyne.TextWrapWord
	// unitIDRow holds the Unit ID entry and the import button; it is
	// disabled for the Tacx app identity, which never uses a Unit ID.
	var setUnitIDEnabled func(bool)

	refreshDeviceInfo := func() {
		c := loadAppConfig()
		if setUnitIDEnabled != nil {
			setUnitIDEnabled(usesUnitID(c))
		}
		switch {
		case !usesUnitID(c):
			deviceInfoLabel.SetText("Rides are uploaded as recorded by Garmin's Tacx Training app. " +
				"Garmin Connect then computes Training Effect, Training Load and VO2max for your " +
				"Garmin devices. The Unit ID below is not used with this option.")
		case c.DeviceSerial != 0:
			txt := fmt.Sprintf("Rides will be attributed to your own device (Unit ID %d)", c.DeviceSerial)
			if c.DeviceSoftwareVersion != 0 {
				txt += fmt.Sprintf(", firmware %d.%02d", c.DeviceSoftwareVersion/100, c.DeviceSoftwareVersion%100)
			}
			deviceInfoLabel.SetText(txt + ". Note: Garmin then treats them as that device's own " +
				"rides and does not update VO2max — use the Tacx option for that.")
		default:
			deviceInfoLabel.SetText("No Unit ID set — a random serial number is used.")
		}
	}

	// updatingDevice suppresses the OnChanged handlers while the UI is being
	// filled in programmatically.
	updatingDevice := false

	deviceSelect.OnChanged = func(label string) {
		if updatingDevice {
			return
		}
		c := loadAppConfig()
		if c.Device == customDeviceKey && label == customLabel(c.DeviceProduct) {
			return
		}
		c.Device = findPresetByLabel(label).Key
		c.DeviceProduct = 0
		// The firmware belongs to the imported model; drop it when switching.
		c.DeviceSoftwareVersion = 0
		saveAppConfig(c)
		updatingDevice = true
		deviceSelect.SetOptions(deviceOptions(c))
		deviceSelect.SetSelected(label)
		updatingDevice = false
		appendLog("Rides will be labelled as: Garmin " + label)
		refreshDeviceInfo()
	}

	unitIDEntry.OnChanged = func(text string) {
		if updatingDevice {
			return
		}
		text = strings.TrimSpace(text)
		c := loadAppConfig()
		if text == "" {
			c.DeviceSerial = 0
		} else {
			v, err := strconv.ParseUint(text, 10, 32)
			if err != nil || v == 0 {
				deviceInfoLabel.SetText("⚠ Unit ID must be a number (digits only), e.g. 3461453371.")
				return
			}
			c.DeviceSerial = uint32(v)
		}
		saveAppConfig(c)
		refreshDeviceInfo()
	}

	loadFitBtn := widget.NewButtonWithIcon("Use my device from a .fit file…", theme.FolderOpenIcon(), nil)
	loadFitBtn.OnTapped = func() {
		fd := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
			if err != nil {
				appendLog("❌ " + err.Error())
				return
			}
			if rc == nil {
				return // cancelled
			}
			defer rc.Close()

			dev, err := readDeviceFromFit(rc)
			if err != nil {
				appendLog("❌ Could not read the device: " + err.Error())
				return
			}
			c := loadAppConfig()
			applyImportedDevice(&c, dev)
			saveAppConfig(c)

			updatingDevice = true
			deviceSelect.SetOptions(deviceOptions(c))
			deviceSelect.SetSelected(deviceFromConfig(c).Name)
			unitIDEntry.SetText(strconv.FormatUint(uint64(c.DeviceSerial), 10))
			updatingDevice = false
			refreshDeviceInfo()

			appendLog("✓ Using your device: " + dev.String())
		}, w)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".fit", ".FIT"}))
		fd.Show()
	}

	setUnitIDEnabled = func(on bool) {
		if on {
			unitIDEntry.Enable()
			loadFitBtn.Enable()
		} else {
			unitIDEntry.Disable()
			loadFitBtn.Disable()
		}
	}
	refreshDeviceInfo()

	rememberCheck := widget.NewCheck(
		"Remember MyWhoosh password (encrypted with your Windows account) — needed for fully automatic sync",
		nil)
	rememberCheck.SetChecked(cfg.RememberMyWhooshPassword)
	rememberCheck.OnChanged = func(on bool) {
		c := loadAppConfig()
		c.RememberMyWhooshPassword = on
		if !on {
			c.MyWhooshPasswordEnc = ""
			appendLog("Stored MyWhoosh password removed.")
		} else {
			appendLog("Enter the MyWhoosh password and click Fetch Activities to store it.")
		}
		saveAppConfig(c)
	}
	if !secretStorageSupported {
		rememberCheck.Disable()
	}

	// --- MFA prompt (called from worker goroutines) ---
	promptMFA := func() (string, error) {
		ch := make(chan string, 1)
		fyne.Do(func() {
			codeEntry := widget.NewEntry()
			codeEntry.SetPlaceHolder("123456")
			dialog.ShowForm("Garmin verification code", "OK", "Cancel",
				[]*widget.FormItem{
					widget.NewFormItem("Code from e-mail / SMS / app", codeEntry),
				},
				func(ok bool) {
					if ok {
						ch <- codeEntry.Text
					} else {
						ch <- ""
					}
				}, w)
		})
		code := <-ch
		if strings.TrimSpace(code) == "" {
			return "", errors.New("cancelled")
		}
		return code, nil
	}

	// readCreds must run on the UI thread (e.g. directly in a button handler).
	readCreds := func() credentials {
		return credentials{
			MyWhooshEmail:    strings.TrimSpace(mwEmailEntry.Text),
			MyWhooshPassword: mwPasswordEntry.Text,
			GarminEmail:      strings.TrimSpace(garminEmailEntry.Text),
			GarminPassword:   garminPasswordEntry.Text,
			PromptMFA:        promptMFA,
		}
	}
	// currentCreds is for worker goroutines.
	currentCreds := func() credentials {
		var c credentials
		fyne.DoAndWait(func() { c = readCreds() })
		return c
	}

	// --- Activity list ---
	activityListBox := container.NewVBox()
	activityScroll := container.NewVScroll(activityListBox)
	activityScroll.SetMinSize(fyne.NewSize(0, 260))

	uploadAllBtn := widget.NewButton("⬆  Upload All to Garmin", nil)
	uploadAllBtn.Importance = widget.HighImportance
	uploadAllBtn.Disable()

	var actMu sync.Mutex
	var currentActivities []mywhoosh.Activity

	var buildActivityRow func(activity mywhoosh.Activity) fyne.CanvasObject

	refreshList := func() {
		actMu.Lock()
		acts := append([]mywhoosh.Activity(nil), currentActivities...)
		actMu.Unlock()

		fyne.Do(func() {
			activityListBox.RemoveAll()
			if len(acts) == 0 {
				activityListBox.Add(widget.NewLabel(
					fmt.Sprintf("No activities found in the last %d days.", loadAppConfig().syncDays())))
			}
			unsynced := 0
			for _, act := range acts {
				activityListBox.Add(buildActivityRow(act))
				if !s.tracker.IsSynced(act.ID) {
					unsynced++
				}
			}
			if unsynced > 0 {
				uploadAllBtn.SetText(fmt.Sprintf("⬆  Upload All to Garmin (%d new)", unsynced))
				uploadAllBtn.Enable()
			} else {
				uploadAllBtn.SetText("✓  All synced")
				uploadAllBtn.Disable()
			}
		})
	}

	setGarminConnected := func() {
		fyne.Do(func() { garminStatusLabel.SetText("✓ Garmin connected") })
	}

	uploadActivity := func(activity mywhoosh.Activity, btn *widget.Button) {
		btn.Disable()
		btn.SetText("⏳")

		go func() {
			defer refreshList()
			if _, err := s.processActivity(activity, currentCreds()); err != nil {
				appendLog("  ❌ " + err.Error())
				return
			}
			setGarminConnected()
		}()
	}

	buildActivityRow = func(activity mywhoosh.Activity) fyne.CanvasObject {
		details := activity.FormattedDate() + "  —  " + activity.DisplayName()
		if dist := activity.FormattedDistance(); dist != "" {
			details += "  •  " + dist
		}
		if dur := activity.FormattedDuration(); dur != "" {
			details += "  •  " + dur
		}
		if activity.AvgPower > 0 {
			details += fmt.Sprintf("  •  %.0fW", activity.AvgPower)
		}
		if activity.AvgHR > 0 {
			details += fmt.Sprintf("  •  %.0fbpm", activity.AvgHR)
		}

		infoLabel := widget.NewLabel(details)
		infoLabel.Wrapping = fyne.TextWrapWord

		uploadBtn := widget.NewButtonWithIcon("⬆ Upload", theme.UploadIcon(), nil)
		uploadBtn.Importance = widget.HighImportance

		act := activity
		uploadBtn.OnTapped = func() { uploadActivity(act, uploadBtn) }

		if s.tracker.IsSynced(activity.ID) {
			// Already synced: offer a re-upload (e.g. after changing the device).
			uploadBtn.SetText("✓ Synced · Re-upload")
			uploadBtn.SetIcon(theme.ViewRefreshIcon())
			uploadBtn.Importance = widget.LowImportance
			uploadBtn.OnTapped = func() {
				dialog.ShowConfirm("Re-upload this ride?",
					"First delete this ride in Garmin Connect.\n\n"+
						"Otherwise Garmin keeps the old copy and the ride is skipped\n"+
						"as already present.\n\nUpload it again now?",
					func(ok bool) {
						if ok {
							uploadActivity(act, uploadBtn)
						}
					}, w)
			}
		}

		return container.NewBorder(nil, nil, nil, uploadBtn, infoLabel)
	}

	// --- Fetch Activities ---
	var fetching bool
	days := cfg.syncDays()
	fetchBtn := widget.NewButton(fmt.Sprintf("📋  Fetch Activities (last %d days)", days), nil)
	fetchBtn.Importance = widget.HighImportance

	fetchBtn.OnTapped = func() {
		if fetching {
			return
		}
		fetching = true
		fetchBtn.Disable()

		creds := readCreds()

		// Persist e-mails
		c := loadAppConfig()
		c.MyWhooshEmail = creds.MyWhooshEmail
		c.GarminEmail = creds.GarminEmail
		saveAppConfig(c)

		go func() {
			defer func() {
				fetching = false
				fyne.Do(func() { fetchBtn.Enable() })
			}()

			// A typed password means: log in fresh (also stores it if asked)
			if creds.MyWhooshPassword != "" {
				s.authMu.Lock()
				err := s.loginMyWhoosh(creds)
				s.authMu.Unlock()
				if err != nil {
					appendLog("❌ " + err.Error())
					return
				}
			}

			acts, err := s.recentActivities(creds, days)
			if err != nil {
				appendLog("❌ Failed to fetch activities: " + err.Error())
				return
			}
			fyne.Do(func() {
				mwStatusLabel.SetText("✓ MyWhoosh connected")
				mwPasswordEntry.SetText("")
			})
			appendLog(fmt.Sprintf("✓ Found %d activities", len(acts)))

			// Connect Garmin as well, so everything is set up in one click
			if _, err := s.ensureGarmin(creds); err != nil {
				if errors.Is(err, errNeedsLogin) {
					appendLog("ℹ Garmin not connected yet — enter Garmin email & password and click Fetch again.")
				} else {
					appendLog("❌ " + err.Error())
				}
			} else {
				setGarminConnected()
				fyne.Do(func() { garminPasswordEntry.SetText("") })
				clearNotice(dir)
			}

			actMu.Lock()
			currentActivities = acts
			actMu.Unlock()
			refreshList()
		}()
	}

	// --- Upload All ---
	var uploadingAll bool
	uploadAllBtn.OnTapped = func() {
		if uploadingAll {
			return
		}
		uploadingAll = true
		uploadAllBtn.Disable()

		go func() {
			defer func() {
				uploadingAll = false
				refreshList()
			}()
			actMu.Lock()
			acts := append([]mywhoosh.Activity(nil), currentActivities...)
			actMu.Unlock()

			sum, err := s.syncPending(acts, currentCreds())
			appendLog("\n✓ Batch complete — " + sum.String())
			if err != nil {
				appendLog("❌ " + err.Error())
			} else {
				setGarminConnected()
			}
		}()
	}

	// --- Automatic sync ---
	autoLabel := widget.NewLabel("")
	autoLabel.Wrapping = fyne.TextWrapWord
	autoBtn := widget.NewButton("", nil)

	refreshAuto := func() {
		if !scheduleSupported {
			autoLabel.SetText("Automatic sync can be switched on here only on Windows.")
			autoBtn.SetText("Not available")
			autoBtn.Disable()
			return
		}
		if scheduleInstalled() {
			autoLabel.SetText(fmt.Sprintf(
				"Automatic sync: ON — every %d minutes while you are logged in to Windows. Log: %s",
				autoSyncMinutes, filepath.Join(dir, logFileName)))
			autoBtn.SetText("Turn off")
		} else {
			autoLabel.SetText("Automatic sync: OFF")
			autoBtn.SetText(fmt.Sprintf("Turn on (every %d min)", autoSyncMinutes))
		}
		autoBtn.Enable()
	}

	autoBtn.OnTapped = func() {
		autoBtn.Disable()
		go func() {
			defer fyne.Do(refreshAuto)

			if scheduleInstalled() {
				if err := removeSchedule(); err != nil {
					appendLog("❌ Could not turn off automatic sync: " + err.Error())
					return
				}
				appendLog("Automatic sync turned off.")
				return
			}

			exe, err := os.Executable()
			if err != nil {
				appendLog("❌ " + err.Error())
				return
			}
			if p, err := filepath.EvalSymlinks(exe); err == nil {
				exe = p
			}
			lower := strings.ToLower(exe)
			if strings.Contains(lower, `\downloads\`) || strings.Contains(lower, `\temp\`) {
				appendLog("⚠ The app runs from " + filepath.Dir(exe) + ". If you move or delete it, " +
					"automatic sync stops working — better move it to a permanent folder first " +
					"and turn automatic sync on from there.")
			}

			if err := installSchedule(exe, autoSyncMinutes); err != nil {
				appendLog("❌ Could not turn on automatic sync: " + err.Error())
				return
			}
			appendLog(fmt.Sprintf("✓ Automatic sync turned on (every %d minutes).", autoSyncMinutes))

			c := loadAppConfig()
			if !c.RememberMyWhooshPassword || c.MyWhooshPasswordEnc == "" {
				appendLog("ℹ Tip: tick \"Remember MyWhoosh password\" and fetch once with the password, " +
					"otherwise you'll be asked to log in when the MyWhoosh session expires.")
			}
		}()
	}
	refreshAuto()

	// --- Layout ---
	title := widget.NewRichTextFromMarkdown("## MyWhoosh → Garmin")

	mywhooshSection := container.NewVBox(
		widget.NewLabel("MyWhoosh Account"),
		mwEmailEntry,
		mwPasswordEntry,
		mwStatusLabel,
	)
	garminSection := container.NewVBox(
		widget.NewLabel("Garmin Connect"),
		garminEmailEntry,
		garminPasswordEntry,
		garminStatusLabel,
	)
	credentialsRow := container.New(layout.NewGridWrapLayout(fyne.NewSize(360, 160)),
		mywhooshSection, garminSection,
	)

	settings := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Show rides in Garmin as:"), nil, deviceSelect),
		container.NewBorder(nil, nil, widget.NewLabel("Unit ID:"), loadFitBtn, unitIDEntry),
		deviceInfoLabel,
		rememberCheck,
	)

	activitiesHeader := container.NewBorder(nil, nil, nil,
		uploadAllBtn,
		widget.NewRichTextFromMarkdown(fmt.Sprintf("### Activities (last %d days)", days)),
	)

	autoRow := container.NewBorder(nil, nil, nil, autoBtn, autoLabel)

	topForm := container.NewVBox(
		title,
		widget.NewSeparator(),
		credentialsRow,
		settings,
		widget.NewSeparator(),
		fetchBtn,
		widget.NewSeparator(),
		activitiesHeader,
	)
	bottom := container.NewVBox(
		widget.NewSeparator(),
		autoRow,
		widget.NewSeparator(),
		logScroll,
	)

	content := container.NewBorder(topForm, bottom, nil, nil, activityScroll)
	w.SetContent(content)
	w.ShowAndRun()
}
