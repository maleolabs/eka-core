package metadata

import (
	"strings"
	"testing"
)

// ptr helpers keep the capture-config cases readable: every field is a
// pointer precisely so "absent" and "explicitly zero" stay distinguishable.
func boolPtr(v bool) *bool        { return &v }
func floatPtr(v float64) *float64 { return &v }
func stringPtr(v string) *string  { return &v }

// TestEffectiveCaptureDefaults: with no capture block at all the ADR-035 v3
// defaults apply. These values are a compatibility contract — a capture run
// that suddenly enabled itself, or deduped over a different window, would
// silently change what gets recorded.
func TestEffectiveCaptureDefaults(t *testing.T) {
	enabled, threshold, window, filter := Metadata{}.EffectiveCapture()
	if !enabled {
		t.Error("enabled default must be true")
	}
	if threshold != 0.6 {
		t.Errorf("threshold default = %v, want 0.6", threshold)
	}
	if window != "24h" {
		t.Errorf("dedupeWindow default = %q, want 24h", window)
	}
	if filter != "all" {
		t.Errorf("provenanceFilterDefault default = %q, want all", filter)
	}
}

// TestEffectiveCaptureOverrides: every field set wins over the default, and a
// partially set block keeps the defaults for the fields it omits. The partial
// case is the one that regresses: an implementation that returns before
// filling the rest would report zero values for untouched fields.
func TestEffectiveCaptureOverrides(t *testing.T) {
	full := Metadata{Capture: &Capture{
		Enabled:                 boolPtr(false),
		Threshold:               floatPtr(0.25),
		DedupeWindow:            stringPtr("90m"),
		ProvenanceFilterDefault: stringPtr("human"),
	}}
	enabled, threshold, window, filter := full.EffectiveCapture()
	if enabled {
		t.Error("enabled override ignored")
	}
	if threshold != 0.25 {
		t.Errorf("threshold = %v, want 0.25", threshold)
	}
	if window != "90m" {
		t.Errorf("dedupeWindow = %q, want 90m", window)
	}
	if filter != "human" {
		t.Errorf("filter = %q, want human", filter)
	}

	partial := Metadata{Capture: &Capture{Threshold: floatPtr(0.9)}}
	enabled, threshold, window, filter = partial.EffectiveCapture()
	if !enabled || threshold != 0.9 || window != "24h" || filter != "all" {
		t.Errorf("partial block = (%v,%v,%q,%q), want (true,0.9,24h,all)",
			enabled, threshold, window, filter)
	}
}

// TestParseDedupeWindow: the grammar Parse enforces must accept the shapes the
// default and the docs use, and refuse anything else rather than silently
// treating a bad window as "no dedupe".
func TestParseDedupeWindow(t *testing.T) {
	valid := []string{"24h", "1h", "30m", "500ms", "10s", "5ns", "1us"}
	for _, s := range valid {
		if got, err := parseDedupeWindow(s); err != nil || got != s {
			t.Errorf("parseDedupeWindow(%q) = (%q,%v), want (%q,nil)", s, got, err, s)
		}
	}
	invalid := []string{"", "24", "h", "24hh", "-1h", "1.5h", "1h30m", "24 hours", "DAY"}
	for _, s := range invalid {
		if _, err := parseDedupeWindow(s); err == nil {
			t.Errorf("parseDedupeWindow(%q) accepted, want refusal", s)
		}
	}
}

// TestParseRejectsInvalidDedupeWindow is the same contract through the public
// Parse, which is the path a real eka.yaml takes: a bad window in the file is
// a load error, not a silent fallback to the default.
func TestParseRejectsInvalidDedupeWindow(t *testing.T) {
	bad := validYAML + "capture:\n  dedupeWindow: nonsense\n"
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("Parse accepted an invalid capture.dedupeWindow")
	}
	good := validYAML + "capture:\n  dedupeWindow: 90m\n"
	m, err := Parse([]byte(good))
	if err != nil {
		t.Fatalf("Parse rejected a valid dedupeWindow: %v", err)
	}
	if _, _, window, _ := m.EffectiveCapture(); window != "90m" {
		t.Errorf("effective dedupeWindow = %q, want 90m", window)
	}
}

// TestParseRejectsInvalidProvenanceFilter: the filter is a closed set, so a
// typo must fail loudly. Accepting it would mean capture silently records
// nothing, or everything, depending on how the typo compared.
func TestParseRejectsInvalidProvenanceFilter(t *testing.T) {
	if _, err := Parse([]byte(validYAML + "capture:\n  provenanceFilterDefault: mostly\n")); err == nil {
		t.Fatal("Parse accepted an invalid capture.provenanceFilterDefault")
	}
	for _, ok := range []string{"all", "human", "inferred", "reconciled"} {
		body := validYAML + "capture:\n  provenanceFilterDefault: " + ok + "\n"
		if _, err := Parse([]byte(body)); err != nil {
			t.Errorf("Parse rejected the valid filter %q: %v", ok, err)
		}
	}
}

// TestMarshalCaptureRoundTrip: Marshal's contract is that Parse reproduces the same
// metadata, so a capture block that renders but does not parse back is a
// defect. Field order inside the block is fixed by declaration.
func TestMarshalCaptureRoundTrip(t *testing.T) {
	cases := []Metadata{
		{Version: 1, Project: "atrium", Name: "api", Namespace: "atrium-api"},
		{
			Version: 1, Project: "atrium", Name: "api", Namespace: "atrium-api",
			Capture: &Capture{
				Enabled:                 boolPtr(true),
				Threshold:               floatPtr(0.6),
				DedupeWindow:            stringPtr("24h"),
				ProvenanceFilterDefault: stringPtr("all"),
			},
		},
		{
			Version: 1, Project: "atrium", Name: "api", Namespace: "atrium-api",
			Capture: &Capture{Threshold: floatPtr(0.35), DedupeWindow: stringPtr("45m")},
		},
	}
	for _, in := range cases {
		out, err := Parse(in.Marshal())
		if err != nil {
			t.Fatalf("Parse(Marshal()) failed for %+v: %v", in, err)
		}
		if out.Version != in.Version || out.Project != in.Project ||
			out.Name != in.Name || out.Namespace != in.Namespace {
			t.Errorf("identity round-trip = %+v, want %+v", out, in)
		}
		inEnabled, inThreshold, inWindow, inFilter := in.EffectiveCapture()
		outEnabled, outThreshold, outWindow, outFilter := out.EffectiveCapture()
		if inEnabled != outEnabled || inThreshold != outThreshold ||
			inWindow != outWindow || inFilter != outFilter {
			t.Errorf("capture round-trip = (%v,%v,%q,%q), want (%v,%v,%q,%q)",
				outEnabled, outThreshold, outWindow, outFilter,
				inEnabled, inThreshold, inWindow, inFilter)
		}
		// Re-marshalling the parsed value must be byte-identical: Marshal is
		// the canonical form, so it has to be a fixed point.
		if again := out.Marshal(); string(again) != string(in.Marshal()) {
			t.Errorf("Marshal not a fixed point:\n first: %q\nsecond: %q", in.Marshal(), again)
		}
	}
}

// TestMarshalRendersOnlyPresentFields: an omitted capture field must not be
// written as a zero value. Writing "threshold: 0" would be a different claim
// from omitting it, and would then parse back as an explicit zero threshold.
func TestMarshalRendersOnlyPresentFields(t *testing.T) {
	m := Metadata{
		Version: 1, Project: "atrium", Name: "api", Namespace: "atrium-api",
		Capture: &Capture{DedupeWindow: stringPtr("90m")},
	}
	got := string(m.Marshal())
	if !strings.Contains(got, "dedupeWindow: 90m") {
		t.Errorf("Marshal dropped the present field:\n%s", got)
	}
	for _, absent := range []string{"enabled:", "threshold:", "provenanceFilterDefault:"} {
		if strings.Contains(got, absent) {
			t.Errorf("Marshal wrote absent field %q:\n%s", absent, got)
		}
	}
}
