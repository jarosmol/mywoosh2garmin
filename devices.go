package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/typedef"
)

// devicePreset is a Garmin device the uploaded rides can claim to come from.
// Garmin Connect only computes Training Effect, Training Load and VO2max for
// activities recorded by a Garmin device, so the FIT file is relabelled.
type devicePreset struct {
	Key     string
	Label   string
	Product typedef.GarminProduct
	// Manufacturer defaults to Garmin when zero.
	Manufacturer typedef.Manufacturer
	// App marks a software recorder (not a physical device): the user's
	// Unit ID is never used with it and it has its own version number.
	App             bool
	SoftwareVersion uint16
}

// manufacturer returns the preset's manufacturer (Garmin by default).
func (p devicePreset) manufacturer() typedef.Manufacturer {
	if p.Manufacturer == 0 {
		return typedef.ManufacturerGarmin
	}
	return p.Manufacturer
}

// tacxKey is the Tacx Training app identity. Garmin Connect post-processes
// rides from this official Garmin app for the user's VO2max-capable device,
// so — unlike a spoofed watch/Edge — such rides do update VO2max. Verified:
// a re-labelled MyWhoosh ride updated cycling VO2max (Nov 2026).
const tacxKey = "tacx"

// devicePresets lists the selectable devices. The first entry is the default.
var devicePresets = []devicePreset{
	{Key: tacxKey, Label: "Tacx Training app (recommended — updates VO2max)",
		Product: typedef.GarminProductTacxTrainingAppWin, Manufacturer: typedef.ManufacturerTacx,
		App: true, SoftwareVersion: 358},
	{Key: "fenix6s", Label: "Fenix 6S", Product: typedef.GarminProductFenix6s},
	{Key: "fenix6", Label: "Fenix 6", Product: typedef.GarminProductFenix6},
	{Key: "fenix7", Label: "Fenix 7", Product: typedef.GarminProductFenix7},
	{Key: "fenix8", Label: "Fenix 8", Product: typedef.GarminProductFenix8},
	{Key: "epix2", Label: "Epix (Gen 2)", Product: typedef.GarminProductEpixGen2},
	{Key: "fr945", Label: "Forerunner 945", Product: typedef.GarminProductFr945},
	{Key: "fr955", Label: "Forerunner 955", Product: typedef.GarminProductFr955},
	{Key: "fr965", Label: "Forerunner 965", Product: typedef.GarminProductFr965},
	{Key: "edge530", Label: "Edge 530", Product: typedef.GarminProductEdge530},
	{Key: "edge830", Label: "Edge 830", Product: typedef.GarminProductEdge830},
	{Key: "edge540", Label: "Edge 540 / 540 Solar", Product: typedef.GarminProductEdge540},
	{Key: "edge840", Label: "Edge 840 / 840 Solar", Product: typedef.GarminProductEdge840},
	{Key: "edge1030", Label: "Edge 1030", Product: typedef.GarminProductEdge1030},
	{Key: "edge1030plus", Label: "Edge 1030 Plus", Product: typedef.GarminProductEdge1030Plus},
	{Key: "edge1040", Label: "Edge 1040", Product: typedef.GarminProductEdge1040},
	{Key: "edge1050", Label: "Edge 1050", Product: typedef.GarminProductEdge1050},
}

// customDeviceKey marks a device imported from a FIT file whose model is not
// in devicePresets.
const customDeviceKey = "custom"

// deviceSpec is what actually gets written into the FIT file.
type deviceSpec struct {
	Name            string
	Manufacturer    typedef.Manufacturer // 0 = Garmin
	Product         uint16
	Serial          uint32 // Garmin "Unit ID"; 0 = keep whatever is in the file
	SoftwareVersion uint16 // firmware ×100 (e.g. 2618 = 26.18); 0 = keep the file's
}

// findPreset returns the preset for key, or the default preset.
func findPreset(key string) devicePreset {
	for _, p := range devicePresets {
		if p.Key == key {
			return p
		}
	}
	return devicePresets[0]
}

// findPresetByLabel returns the preset whose label matches, or the default.
func findPresetByLabel(label string) devicePreset {
	for _, p := range devicePresets {
		if p.Label == label {
			return p
		}
	}
	return devicePresets[0]
}

// findPresetByProduct returns the physical-device preset for a Garmin
// product id, if listed.
func findPresetByProduct(product uint16) (devicePreset, bool) {
	for _, p := range devicePresets {
		if !p.App && p.Product.Uint16() == product {
			return p, true
		}
	}
	return devicePreset{}, false
}

func presetLabels() []string {
	labels := make([]string, len(devicePresets))
	for i, p := range devicePresets {
		labels[i] = p.Label
	}
	return labels
}

// customLabel is how a non-listed imported device appears in the dropdown.
func customLabel(product uint16) string {
	return fmt.Sprintf("Garmin %s (from your .fit file)", typedef.GarminProduct(product))
}

// deviceFromConfig builds the device spec from the saved configuration.
// For a physical device the serial is the user's Unit ID if set, otherwise
// the generated one. An app identity (Tacx) always uses the generated serial
// and its own version: using the Unit ID of the user's Edge would make
// Garmin treat the ride as the Edge's own recording.
func deviceFromConfig(cfg appConfig) deviceSpec {
	spec := deviceSpec{Manufacturer: typedef.ManufacturerGarmin}

	if cfg.Device == customDeviceKey && cfg.DeviceProduct != 0 {
		spec.Product = cfg.DeviceProduct
		spec.Name = customLabel(cfg.DeviceProduct)
	} else {
		p := findPreset(cfg.Device)
		spec.Product = p.Product.Uint16()
		spec.Name = p.Label
		spec.Manufacturer = p.manufacturer()
		if p.App {
			spec.Serial = cfg.GeneratedSerial
			spec.SoftwareVersion = p.SoftwareVersion
			return spec
		}
	}

	spec.SoftwareVersion = cfg.DeviceSoftwareVersion
	spec.Serial = cfg.DeviceSerial
	if spec.Serial == 0 {
		spec.Serial = cfg.GeneratedSerial
	}
	return spec
}

// usesUnitID reports whether the current choice uses the user's Unit ID.
func usesUnitID(cfg appConfig) bool {
	if cfg.Device == customDeviceKey {
		return true
	}
	return !findPreset(cfg.Device).App
}

// currentDevice loads the configuration, makes sure a generated serial
// number exists (saving it on first use) and returns the device to use.
func currentDevice() deviceSpec {
	cfg := loadAppConfig()
	if cfg.GeneratedSerial == 0 {
		cfg.GeneratedSerial = randomSerial()
		saveAppConfig(cfg)
	}
	return deviceFromConfig(cfg)
}

// randomSerial returns a random 10-digit serial number used when the user
// has not entered their own device's Unit ID. Previously every user shared
// one fixed number, which Garmin Connect matched to some stranger's watch.
func randomSerial() uint32 {
	const lo, span = 1_000_000_000, 3_000_000_000
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fakeSerialNumber
	}
	return lo + binary.LittleEndian.Uint32(b[:])%span
}

// importedDevice is the identity of a real Garmin device, read from a FIT
// file it recorded.
type importedDevice struct {
	Product         uint16
	Serial          uint32
	SoftwareVersion uint16
}

func (d importedDevice) String() string {
	fw := "unknown"
	if d.SoftwareVersion != 0 && d.SoftwareVersion != ^uint16(0) {
		fw = fmt.Sprintf("%d.%02d", d.SoftwareVersion/100, d.SoftwareVersion%100)
	}
	return fmt.Sprintf("Garmin %s — Unit ID %d, firmware %s",
		typedef.GarminProduct(d.Product), d.Serial, fw)
}

// readDeviceFromFit reads the recording device (model, Unit ID, firmware)
// from a FIT activity recorded on a Garmin device.
func readDeviceFromFit(r io.Reader) (importedDevice, error) {
	var d importedDevice

	lis := filedef.NewListener()
	defer lis.Close()
	dec := decoder.New(r, decoder.WithMesgListener(lis), decoder.WithBroadcastOnly())
	if _, err := dec.Decode(); err != nil {
		return d, fmt.Errorf("not a readable .fit file: %w", err)
	}

	act, ok := lis.File().(*filedef.Activity)
	if !ok {
		return d, fmt.Errorf("this .fit file is not an activity — export a ride recorded on your device")
	}

	fid := act.FileId
	if fid.Manufacturer != typedef.ManufacturerGarmin {
		return d, fmt.Errorf("this activity was not recorded on a Garmin device (manufacturer: %s)", fid.Manufacturer)
	}
	if fid.Product == 0 || fid.Product == ^uint16(0) {
		return d, fmt.Errorf("the file does not say which Garmin model recorded it")
	}
	if fid.SerialNumber == 0 || fid.SerialNumber == ^uint32(0) {
		return d, fmt.Errorf("the file does not contain the device's Unit ID")
	}
	d.Product = fid.Product
	d.Serial = fid.SerialNumber

	// Firmware: the creator's device_info (index 0), or any entry of the
	// same model and serial.
	for _, di := range act.DeviceInfos {
		if di.Manufacturer != typedef.ManufacturerGarmin || di.Product != d.Product {
			continue
		}
		if di.SoftwareVersion == 0 || di.SoftwareVersion == ^uint16(0) {
			continue
		}
		if di.DeviceIndex == typedef.DeviceIndexCreator || di.SerialNumber == d.Serial {
			d.SoftwareVersion = di.SoftwareVersion
			break
		}
	}
	return d, nil
}

// applyImportedDevice stores an imported device in the configuration.
func applyImportedDevice(cfg *appConfig, d importedDevice) {
	if p, ok := findPresetByProduct(d.Product); ok {
		cfg.Device = p.Key
		cfg.DeviceProduct = 0
	} else {
		cfg.Device = customDeviceKey
		cfg.DeviceProduct = d.Product
	}
	cfg.DeviceSerial = d.Serial
	cfg.DeviceSoftwareVersion = d.SoftwareVersion
}
