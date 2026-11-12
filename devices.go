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
}

// devicePresets lists the selectable devices. The first entry is the default
// (it is what the original tool used and is known to work).
var devicePresets = []devicePreset{
	{"fenix6s", "Fenix 6S (default)", typedef.GarminProductFenix6s},
	{"fenix6", "Fenix 6", typedef.GarminProductFenix6},
	{"fenix7", "Fenix 7", typedef.GarminProductFenix7},
	{"fenix8", "Fenix 8", typedef.GarminProductFenix8},
	{"epix2", "Epix (Gen 2)", typedef.GarminProductEpixGen2},
	{"fr945", "Forerunner 945", typedef.GarminProductFr945},
	{"fr955", "Forerunner 955", typedef.GarminProductFr955},
	{"fr965", "Forerunner 965", typedef.GarminProductFr965},
	{"edge530", "Edge 530", typedef.GarminProductEdge530},
	{"edge830", "Edge 830", typedef.GarminProductEdge830},
	{"edge540", "Edge 540 / 540 Solar", typedef.GarminProductEdge540},
	{"edge840", "Edge 840 / 840 Solar", typedef.GarminProductEdge840},
	{"edge1030", "Edge 1030", typedef.GarminProductEdge1030},
	{"edge1030plus", "Edge 1030 Plus", typedef.GarminProductEdge1030Plus},
	{"edge1040", "Edge 1040", typedef.GarminProductEdge1040},
	{"edge1050", "Edge 1050", typedef.GarminProductEdge1050},
}

// customDeviceKey marks a device imported from a FIT file whose model is not
// in devicePresets.
const customDeviceKey = "custom"

// deviceSpec is what actually gets written into the FIT file.
type deviceSpec struct {
	Name            string
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

// findPresetByProduct returns the preset for a Garmin product id, if listed.
func findPresetByProduct(product uint16) (devicePreset, bool) {
	for _, p := range devicePresets {
		if p.Product.Uint16() == product {
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
// The serial is the user's Unit ID if set, otherwise the generated one.
func deviceFromConfig(cfg appConfig) deviceSpec {
	spec := deviceSpec{SoftwareVersion: cfg.DeviceSoftwareVersion}

	if cfg.Device == customDeviceKey && cfg.DeviceProduct != 0 {
		spec.Product = cfg.DeviceProduct
		spec.Name = customLabel(cfg.DeviceProduct)
	} else {
		p := findPreset(cfg.Device)
		spec.Product = p.Product.Uint16()
		spec.Name = p.Label
	}

	spec.Serial = cfg.DeviceSerial
	if spec.Serial == 0 {
		spec.Serial = cfg.GeneratedSerial
	}
	return spec
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
