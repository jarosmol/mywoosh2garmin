package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"
)

// FIT protocol invalid sentinel values per base type.
const (
	uint8Invalid  = uint8(0xFF)
	uint16Invalid = uint16(0xFFFF)
	sint8Invalid  = int8(0x7F)
)

// Device spoofing constants.
const (
	garminManufacturer = typedef.ManufacturerGarmin // 1
	// fakeSerialNumber was shared by every user of the original tool and is
	// only a last-resort fallback now (see randomSerial / Unit ID setting).
	fakeSerialNumber = uint32(3420897194)
)

// rideInfo describes when the ride took place (UTC), taken from the FIT records.
type rideInfo struct {
	Start time.Time
	End   time.Time
}

// logFn can be overridden to redirect log output (e.g., to a GUI).
var logFn = func(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}

// ---------------------------------------------------------------------------
// MyWhoosh directory detection
// ---------------------------------------------------------------------------

func findMyWhooshDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	switch runtime.GOOS {
	case "darwin":
		p := filepath.Join(home,
			"Library", "Containers", "com.whoosh.whooshgame",
			"Data", "Library", "Application Support",
			"Epic", "MyWhoosh", "Content", "Data")
		if isDir(p) {
			return p, nil
		}
		return "", fmt.Errorf("not found: %s", p)

	case "windows":
		base := filepath.Join(home, "AppData", "Local", "Packages")
		entries, err := os.ReadDir(base)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "MyWhooshTechnologyService.") {
				p := filepath.Join(base, e.Name(),
					"LocalCache", "Local", "MyWhoosh", "Content", "Data")
				if isDir(p) {
					return p, nil
				}
			}
		}
		return "", fmt.Errorf("MyWhoosh not found in %s", base)

	default:
		return "", fmt.Errorf("no auto-detection for %s — use the directory picker", runtime.GOOS)
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ---------------------------------------------------------------------------
// FIT file discovery
// ---------------------------------------------------------------------------

// findMostRecentFitFile returns the MyNewActivity-*.fit file with the highest
// version number (e.g. MyNewActivity-3.8.5.fit > MyNewActivity-3.7.0.fit).
func findMostRecentFitFile(dir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "MyNewActivity-*.fit"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no MyNewActivity-*.fit files in %s", dir)
	}

	re := regexp.MustCompile(`\d+`)
	sort.Slice(matches, func(i, j int) bool {
		vi := re.FindAllString(filepath.Base(matches[i]), -1)
		vj := re.FindAllString(filepath.Base(matches[j]), -1)
		return cmpVersionParts(vi, vj) > 0
	})

	return matches[0], nil
}

func cmpVersionParts(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		ai, _ := strconv.Atoi(a[i])
		bi, _ := strconv.Atoi(b[i])
		if ai != bi {
			return ai - bi
		}
	}
	return len(a) - len(b)
}

func generateOutputFilename(inputPath string) string {
	name := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	ts := time.Now().Format("2006-01-02_150405")
	return fmt.Sprintf("%s_%s.fit", name, ts)
}

// ---------------------------------------------------------------------------
// FIT file processing
// ---------------------------------------------------------------------------

// fixFitFile reads a MyWhoosh FIT activity, fixes missing session averages,
// strips temperature from records, spoofs the device, and writes the result.
// It returns the start and end time of the ride.
func fixFitFile(inputPath, outputPath string, dev deviceSpec) (rideInfo, error) {
	var info rideInfo

	f, err := os.Open(inputPath)
	if err != nil {
		return info, err
	}
	defer f.Close()

	lis := filedef.NewListener()
	defer lis.Close()

	dec := decoder.New(f,
		decoder.WithMesgListener(lis),
		decoder.WithBroadcastOnly(),
	)

	_, err = dec.Decode()
	if err != nil {
		return info, fmt.Errorf("decode: %w", err)
	}

	activity, ok := lis.File().(*filedef.Activity)
	if !ok {
		return info, fmt.Errorf("not an activity file (got %T)", lis.File())
	}

	// Collect metrics from records and strip temperature
	var powers []uint16
	var heartRates, cadences []uint8

	for _, rec := range activity.Records {
		if !rec.Timestamp.IsZero() {
			ts := rec.Timestamp.UTC()
			if info.Start.IsZero() || ts.Before(info.Start) {
				info.Start = ts
			}
			if ts.After(info.End) {
				info.End = ts
			}
		}
		if rec.Power != uint16Invalid {
			powers = append(powers, rec.Power)
		}
		if rec.HeartRate != uint8Invalid {
			heartRates = append(heartRates, rec.HeartRate)
		}
		if rec.Cadence != uint8Invalid {
			cadences = append(cadences, rec.Cadence)
		}
		rec.Temperature = sint8Invalid
	}

	logFn("Records: %d | Power: %d | HR: %d | Cadence: %d samples\n",
		len(activity.Records), len(powers), len(heartRates), len(cadences))

	// Fix missing session averages
	for _, sess := range activity.Sessions {
		if shouldFixU16(sess.AvgPower) && len(powers) > 0 {
			sess.AvgPower = avgU16(powers)
			logFn("  → avg power:      %d W\n", sess.AvgPower)
		}
		if shouldFixU8(sess.AvgHeartRate) && len(heartRates) > 0 {
			sess.AvgHeartRate = avgU8(heartRates)
			logFn("  → avg heart rate: %d bpm\n", sess.AvgHeartRate)
		}
		if shouldFixU8(sess.AvgCadence) && len(cadences) > 0 {
			sess.AvgCadence = avgU8(cadences)
			logFn("  → avg cadence:    %d rpm\n", sess.AvgCadence)
		}
	}

	// Fall back to the session times if records carried no timestamps
	if info.Start.IsZero() {
		for _, sess := range activity.Sessions {
			if !sess.StartTime.IsZero() {
				info.Start = sess.StartTime.UTC()
				if sess.TotalElapsedTime != 0 && sess.TotalElapsedTime != ^uint32(0) {
					info.End = info.Start.Add(
						time.Duration(sess.TotalElapsedTime) * time.Millisecond)
				}
				break
			}
		}
	}

	// Spoof device
	spoofDevice(activity, dev)

	// Encode
	fit := activity.ToFIT(nil)

	out, err := os.Create(outputPath)
	if err != nil {
		return info, err
	}

	if err := encoder.New(out, encoder.WithProtocolVersion(proto.V2)).Encode(&fit); err != nil {
		out.Close()
		return info, err
	}
	return info, out.Close()
}

func shouldFixU16(v uint16) bool { return v == uint16Invalid || v == 0 }
func shouldFixU8(v uint8) bool   { return v == uint8Invalid || v == 0 }

func avgU16(vals []uint16) uint16 {
	var sum uint64
	for _, v := range vals {
		sum += uint64(v)
	}
	return uint16(sum / uint64(len(vals)))
}

func avgU8(vals []uint8) uint8 {
	var sum uint64
	for _, v := range vals {
		sum += uint64(v)
	}
	return uint8(sum / uint64(len(vals)))
}

// ---------------------------------------------------------------------------
// Device spoofing
// ---------------------------------------------------------------------------

func spoofDevice(activity *filedef.Activity, dev deviceSpec) {
	serial := dev.Serial
	if serial == 0 {
		serial = fakeSerialNumber
	}
	manuf := dev.Manufacturer
	if manuf == 0 {
		manuf = garminManufacturer
	}

	activity.FileId.Manufacturer = manuf
	activity.FileId.Product = dev.Product
	activity.FileId.SerialNumber = serial
	activity.FileId.ProductName = ""

	hasCreator := false
	for _, di := range activity.DeviceInfos {
		di.Manufacturer = manuf
		di.Product = dev.Product
		di.SerialNumber = serial
		di.ProductName = ""
		if dev.SoftwareVersion != 0 {
			di.SoftwareVersion = dev.SoftwareVersion
		}
		if di.DeviceIndex == typedef.DeviceIndexCreator {
			hasCreator = true
		}
	}

	// MyWhoosh files carry no device_info at all, while every file recorded
	// on a Garmin has one for the recording device (index 0). Add it, the
	// way an Edge writes it, so the file looks like a genuine recording.
	if !hasCreator {
		ts := activity.FileId.TimeCreated
		if len(activity.Records) > 0 && !activity.Records[0].Timestamp.IsZero() {
			ts = activity.Records[0].Timestamp
		}
		di := mesgdef.NewDeviceInfo(nil).
			SetTimestamp(ts).
			SetDeviceIndex(typedef.DeviceIndexCreator).
			SetDeviceType(255).
			SetManufacturer(manuf).
			SetProduct(dev.Product).
			SetSerialNumber(serial).
			SetSourceType(typedef.SourceTypeLocal)
		if dev.SoftwareVersion != 0 {
			di.SetSoftwareVersion(dev.SoftwareVersion)
		}
		activity.DeviceInfos = append([]*mesgdef.DeviceInfo{di}, activity.DeviceInfos...)
	}

	fw := ""
	if dev.SoftwareVersion != 0 {
		fw = fmt.Sprintf(", firmware %d.%02d", dev.SoftwareVersion/100, dev.SoftwareVersion%100)
	}
	logFn("  → device spoofed: %s (product %d, serial %d%s)\n",
		dev.Name, dev.Product, serial, fw)
}

// ---------------------------------------------------------------------------
// Sync helpers
// ---------------------------------------------------------------------------

// findUnsyncedFitFiles returns *.fit files modified in the last 30 days
// that don't have a .synced marker file next to them.
func findUnsyncedFitFiles(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.fit"))
	if err != nil {
		return nil, err
	}

	cutoff := time.Now().AddDate(0, 0, -30)
	var result []string

	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			continue
		}
		if isSynced(path) {
			continue
		}
		result = append(result, path)
	}

	// Sort oldest first so we upload in chronological order
	sort.Slice(result, func(i, j int) bool {
		ii, _ := os.Stat(result[i])
		jj, _ := os.Stat(result[j])
		return ii.ModTime().Before(jj.ModTime())
	})

	return result, nil
}

// isSynced checks if a .synced marker file exists for the given FIT file.
func isSynced(fitPath string) bool {
	_, err := os.Stat(fitPath + ".synced")
	return err == nil
}

// markSynced creates a .synced marker file next to the FIT file.
func markSynced(fitPath string) error {
	return os.WriteFile(fitPath+".synced", []byte(time.Now().Format(time.RFC3339)), 0o644)
}
