package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"

	"mywhoosh2garmin/garmin"
)

// createTestFitFile creates a synthetic MyWhoosh-like FIT file with:
//   - Records that have power, HR, cadence, and temperature set
//   - A session with NO average power, HR, or cadence (simulating the MyWhoosh bug)
func createTestFitFile(t *testing.T, path string) {
	t.Helper()

	now := time.Now()
	activity := filedef.NewActivity()

	activity.FileId.
		SetType(typedef.FileActivity).
		SetTimeCreated(now).
		SetManufacturer(typedef.ManufacturerDevelopment).
		SetProduct(0)

	// Add records with known values: power=200, HR=150, cadence=90, temp=25
	for i := 0; i < 10; i++ {
		rec := mesgdef.NewRecord(nil).
			SetTimestamp(now.Add(time.Duration(i) * time.Second)).
			SetPower(200).
			SetHeartRate(150).
			SetCadence(90).
			SetTemperature(25)
		activity.Records = append(activity.Records, rec)
	}

	// Add a lap
	activity.Laps = append(activity.Laps,
		mesgdef.NewLap(nil).
			SetTimestamp(now.Add(10*time.Second)).
			SetStartTime(now),
	)

	// Add a session with NO averages set (mimics the MyWhoosh bug)
	activity.Sessions = append(activity.Sessions,
		mesgdef.NewSession(nil).
			SetTimestamp(now.Add(10*time.Second)).
			SetStartTime(now).
			SetSport(typedef.SportCycling),
		// AvgPower, AvgHeartRate, AvgCadence are all left at invalid
	)

	activity.Activity = mesgdef.NewActivity(nil).
		SetTimestamp(now.Add(10 * time.Second)).
		SetType(typedef.ActivityManual).
		SetNumSessions(1)

	fit := activity.ToFIT(nil)

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := encoder.New(f).Encode(&fit); err != nil {
		t.Fatal(err)
	}
}

func TestFixFitFile(t *testing.T) {
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "MyNewActivity-3.8.5.fit")
	outputPath := filepath.Join(tmpDir, "MyNewActivity-3.8.5_fixed.fit")

	// Create synthetic MyWhoosh FIT file
	createTestFitFile(t, inputPath)

	// Run the fixer
	dev := deviceFromConfig(appConfig{
		Device:                "edge840",
		DeviceSerial:          3461453371,
		DeviceSoftwareVersion: 2618,
	})
	info, err := fixFitFile(inputPath, outputPath, dev)
	if err != nil {
		t.Fatalf("fixFitFile failed: %v", err)
	}

	// Ride time comes from the records (10 records, 1 s apart)
	if info.Start.IsZero() || info.End.IsZero() {
		t.Fatalf("ride time not detected: %+v", info)
	}
	if got := info.End.Sub(info.Start); got != 9*time.Second {
		t.Errorf("ride duration: got %v, want 9s", got)
	}

	// Read back and verify
	f, err := os.Open(outputPath)
	if err != nil {
		t.Fatal(err)
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
		t.Fatalf("decode fixed file: %v", err)
	}

	result, ok := lis.File().(*filedef.Activity)
	if !ok {
		t.Fatalf("expected activity file, got %T", lis.File())
	}

	// Verify temperature was removed from all records
	for i, rec := range result.Records {
		if rec.Temperature != sint8Invalid {
			t.Errorf("record %d: temperature not removed (got %d)", i, rec.Temperature)
		}
	}

	// Verify session averages were set
	if len(result.Sessions) == 0 {
		t.Fatal("no sessions in output")
	}
	sess := result.Sessions[0]

	if sess.AvgPower != 200 {
		t.Errorf("avg power: got %d, want 200", sess.AvgPower)
	}
	if sess.AvgHeartRate != 150 {
		t.Errorf("avg HR: got %d, want 150", sess.AvgHeartRate)
	}
	if sess.AvgCadence != 90 {
		t.Errorf("avg cadence: got %d, want 90", sess.AvgCadence)
	}

	// Verify the device was relabelled as the chosen Garmin model
	if result.FileId.Manufacturer != typedef.ManufacturerGarmin {
		t.Errorf("manufacturer: got %v, want garmin", result.FileId.Manufacturer)
	}
	if result.FileId.Product != typedef.GarminProductEdge840.Uint16() {
		t.Errorf("product: got %d, want %d (Edge 840)",
			result.FileId.Product, typedef.GarminProductEdge840.Uint16())
	}
	if result.FileId.SerialNumber != 3461453371 {
		t.Errorf("serial: got %d, want the configured Unit ID", result.FileId.SerialNumber)
	}
	// The MyWhoosh-like input has no device_info: a creator entry is added
	if len(result.DeviceInfos) != 1 {
		t.Fatalf("device_info count: got %d, want 1 (added creator)", len(result.DeviceInfos))
	}
	for i, di := range result.DeviceInfos {
		if di.DeviceIndex != typedef.DeviceIndexCreator || di.Manufacturer != typedef.ManufacturerGarmin ||
			di.Product != typedef.GarminProductEdge840.Uint16() ||
			di.SerialNumber != 3461453371 || di.SoftwareVersion != 2618 {
			t.Errorf("device_info[%d]: %+v, want creator Edge 840 with Unit ID and firmware 26.18", i, di)
		}
	}
}

func TestDevicePresets(t *testing.T) {
	// Unknown / empty keys fall back to the original default (Fenix 6S)
	for _, key := range []string{"", "nonsense"} {
		if p := findPreset(key); p.Product != typedef.GarminProductFenix6s {
			t.Errorf("findPreset(%q) = %v, want Fenix 6S default", key, p.Label)
		}
	}
	seen := map[string]bool{}
	for _, p := range devicePresets {
		if seen[p.Key] {
			t.Errorf("duplicate preset key %q", p.Key)
		}
		seen[p.Key] = true
		if findPresetByLabel(p.Label).Key != p.Key {
			t.Errorf("label %q does not round-trip", p.Label)
		}
		if p.Product.Uint16() == 0 {
			t.Errorf("preset %q has no product id", p.Key)
		}
	}
	// Serial override from config
	// Unit ID wins over the generated serial; generated one is the fallback
	if d := deviceFromConfig(appConfig{DeviceSerial: 12345, GeneratedSerial: 999}); d.Serial != 12345 {
		t.Errorf("Unit ID ignored: %d", d.Serial)
	}
	if d := deviceFromConfig(appConfig{GeneratedSerial: 999}); d.Serial != 999 {
		t.Errorf("generated serial ignored: %d", d.Serial)
	}
	// Custom (non-listed) imported model
	d := deviceFromConfig(appConfig{Device: customDeviceKey, DeviceProduct: 3992})
	if d.Product != 3992 || d.Name != customLabel(3992) {
		t.Errorf("custom device: got %+v", d)
	}
}

func TestRandomSerial(t *testing.T) {
	seen := map[uint32]bool{}
	for i := 0; i < 50; i++ {
		s := randomSerial()
		if s < 1_000_000_000 || s == fakeSerialNumber {
			t.Fatalf("bad serial %d", s)
		}
		seen[s] = true
	}
	if len(seen) < 45 {
		t.Errorf("serials not random enough: %d unique of 50", len(seen))
	}
}

func TestCurrentDeviceGeneratesSerialOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	a := currentDevice()
	b := currentDevice()
	if a.Serial == 0 || a.Serial != b.Serial {
		t.Errorf("generated serial not stable: %d vs %d", a.Serial, b.Serial)
	}
	if loadAppConfig().GeneratedSerial != a.Serial {
		t.Error("generated serial not saved")
	}
}

// writeFit encodes an activity to a temp file and returns its path.
func writeFit(t *testing.T, act *filedef.Activity) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dev.fit")
	fit := act.ToFIT(nil)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := encoder.New(f).Encode(&fit); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadDeviceFromFit(t *testing.T) {
	now := time.Now()
	act := filedef.NewActivity()
	act.FileId.SetType(typedef.FileActivity).SetTimeCreated(now).
		SetManufacturer(typedef.ManufacturerGarmin).
		SetProduct(typedef.GarminProductEdge540.Uint16()).
		SetSerialNumber(3461453371)
	// A sensor first (must be ignored), then the creator
	act.DeviceInfos = append(act.DeviceInfos,
		mesgdef.NewDeviceInfo(nil).SetTimestamp(now).SetDeviceIndex(4).
			SetManufacturer(typedef.ManufacturerGarmin).SetProduct(3592).
			SetSerialNumber(3453416087).SetSoftwareVersion(336),
		mesgdef.NewDeviceInfo(nil).SetTimestamp(now).SetDeviceIndex(typedef.DeviceIndexCreator).
			SetManufacturer(typedef.ManufacturerGarmin).SetProduct(typedef.GarminProductEdge540.Uint16()).
			SetSerialNumber(3461453371).SetSoftwareVersion(2618),
	)
	act.Records = append(act.Records, mesgdef.NewRecord(nil).SetTimestamp(now).SetPower(100))
	act.Sessions = append(act.Sessions, mesgdef.NewSession(nil).SetTimestamp(now).
		SetStartTime(now).SetSport(typedef.SportCycling))
	act.Activity = mesgdef.NewActivity(nil).SetTimestamp(now).SetNumSessions(1)

	f, err := os.Open(writeFit(t, act))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := readDeviceFromFit(f)
	if err != nil {
		t.Fatalf("readDeviceFromFit: %v", err)
	}
	want := importedDevice{Product: 4061, Serial: 3461453371, SoftwareVersion: 2618}
	if d != want {
		t.Errorf("got %+v, want %+v", d, want)
	}

	// Applying it selects the Edge 540 preset with the Unit ID and firmware
	var cfg appConfig
	applyImportedDevice(&cfg, d)
	spec := deviceFromConfig(cfg)
	if cfg.Device != "edge540" || spec.Serial != 3461453371 || spec.SoftwareVersion != 2618 {
		t.Errorf("applied config wrong: %+v / %+v", cfg, spec)
	}

	// A non-Garmin file is rejected
	act.FileId.SetManufacturer(typedef.ManufacturerDevelopment)
	f2, _ := os.Open(writeFit(t, act))
	defer f2.Close()
	if _, err := readDeviceFromFit(f2); err == nil {
		t.Error("expected error for a non-Garmin file")
	}
}

// TestReadDeviceFromRealFit runs against a real device file when
// M2G_REAL_FIT points to one (not part of the repository).
func TestReadDeviceFromRealFit(t *testing.T) {
	p := os.Getenv("M2G_REAL_FIT")
	if p == "" {
		t.Skip("set M2G_REAL_FIT to a .fit file recorded on a Garmin device")
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := readDeviceFromFit(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("read: %s (product %d)", d, d.Product)
}

func TestOverlapFraction(t *testing.T) {
	base := time.Date(2026, 11, 6, 17, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }

	cases := []struct {
		name           string
		aS, aE, bS, bE int
		want           float64
	}{
		{"identical", 0, 60, 0, 60, 1},
		{"watch started 2 min earlier", 2, 62, 0, 61, 59.0 / 60.0},
		{"no overlap", 0, 60, 90, 120, 0},
		{"touching", 0, 60, 60, 90, 0},
		{"short run inside long ride", 0, 120, 30, 60, 1},
		{"small overlap", 0, 60, 50, 110, 10.0 / 60.0},
	}
	for _, c := range cases {
		got := overlapFraction(at(c.aS), at(c.aE), at(c.bS), at(c.bE))
		if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s: got %.4f, want %.4f", c.name, got, c.want)
		}
	}

	ride := rideInfo{Start: at(0), End: at(60)}
	existing := []garmin.ExistingActivity{
		{ID: 1, Name: "Morning Run", Start: at(-180), End: at(-120)},
		{ID: 2, Name: "Indoor Cycling", Start: at(1), End: at(59)},
	}
	if hit, ok := findOverlap(ride, existing); !ok || hit.ID != 2 {
		t.Errorf("findOverlap: got %+v %v, want activity 2", hit, ok)
	}
	if _, ok := findOverlap(ride, existing[:1]); ok {
		t.Error("findOverlap: unexpected match with unrelated activity")
	}
}

func TestFindMostRecentFitFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create fake FIT files with different version numbers
	files := []string{
		"MyNewActivity-3.7.0.fit",
		"MyNewActivity-3.8.5.fit",
		"MyNewActivity-3.8.1.fit",
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := findMostRecentFitFile(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(tmpDir, "MyNewActivity-3.8.5.fit")
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestGenerateOutputFilename(t *testing.T) {
	name := generateOutputFilename("/some/path/MyNewActivity-3.8.5.fit")
	if !contains(name, "MyNewActivity-3.8.5_") || !contains(name, ".fit") {
		t.Errorf("unexpected filename: %s", name)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstr(s, substr)
}

func searchSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
