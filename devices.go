package main

import "github.com/muktihari/fit/profile/typedef"

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
	{"edge540", "Edge 540", typedef.GarminProductEdge540},
	{"edge840", "Edge 840", typedef.GarminProductEdge840},
	{"edge1030", "Edge 1030", typedef.GarminProductEdge1030},
	{"edge1030plus", "Edge 1030 Plus", typedef.GarminProductEdge1030Plus},
	{"edge1040", "Edge 1040", typedef.GarminProductEdge1040},
	{"edge1050", "Edge 1050", typedef.GarminProductEdge1050},
}

// deviceSpec is what actually gets written into the FIT file.
type deviceSpec struct {
	Name    string
	Product uint16
	Serial  uint32 // 0 = use the built-in fake serial number
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

func presetLabels() []string {
	labels := make([]string, len(devicePresets))
	for i, p := range devicePresets {
		labels[i] = p.Label
	}
	return labels
}

// deviceFromConfig builds the device spec from the saved configuration.
func deviceFromConfig(cfg appConfig) deviceSpec {
	p := findPreset(cfg.Device)
	return deviceSpec{
		Name:    p.Label,
		Product: p.Product.Uint16(),
		Serial:  cfg.DeviceSerial,
	}
}
