package registry

import (
	"errors"
	"testing"
)

func testPresets(t *testing.T) *Presets {
	t.Helper()
	p := NewPresets("build", "lint", "test")
	if err := p.Add("default", "build", "lint"); err != nil {
		t.Fatalf("Add default: %v", err)
	}
	return p
}

func TestPresetsResolveAlias(t *testing.T) {
	p := testPresets(t)
	got, err := p.Resolve("default")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "build" {
		t.Fatalf("Resolve = %q, want %q", got, "build")
	}
}

func TestPresetsRejectUnknownPreset(t *testing.T) {
	p := testPresets(t)
	got, err := p.Resolve("missing")
	if !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("Resolve error = %v, want ErrUnknownPreset", err)
	}
	if got != "" {
		t.Fatalf("Resolve of unknown preset = %q, want empty", got)
	}
	if _, err := p.Recommended("missing"); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("Recommended error = %v, want ErrUnknownPreset", err)
	}
}

func TestPresetsRequireExistingAlias(t *testing.T) {
	p := NewPresets("build")
	if err := p.Add("broken", "missing", ""); !errors.Is(err, ErrUnknownID) {
		t.Fatalf("Add with unknown alias = %v, want ErrUnknownID", err)
	}
	if _, err := p.Resolve("broken"); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("Resolve of rejected preset = %v, want ErrUnknownPreset", err)
	}
}

func TestPresetsRequireExistingRecommended(t *testing.T) {
	p := NewPresets("build")
	if err := p.Add("broken", "build", "missing"); !errors.Is(err, ErrUnknownID) {
		t.Fatalf("Add with unknown recommended = %v, want ErrUnknownID", err)
	}
	if _, err := p.Resolve("broken"); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("Resolve of rejected preset = %v, want ErrUnknownPreset", err)
	}
}

func TestPresetsRecommendedIsOneID(t *testing.T) {
	p := testPresets(t)
	got, err := p.Recommended("default")
	if err != nil {
		t.Fatalf("Recommended: %v", err)
	}
	if got != "lint" {
		t.Fatalf("Recommended = %q, want %q", got, "lint")
	}

	if err := p.Add("plain", "test", ""); err != nil {
		t.Fatalf("Add plain: %v", err)
	}
	got, err = p.Recommended("plain")
	if err != nil {
		t.Fatalf("Recommended plain: %v", err)
	}
	if got != "" {
		t.Fatalf("Recommended plain = %q, want empty", got)
	}
}

func TestPresetsRejectEmptyName(t *testing.T) {
	p := NewPresets("build")
	if err := p.Add("", "build", ""); err == nil {
		t.Fatal("Add with empty name succeeded, want error")
	}
}

func TestPresetsZeroValue(t *testing.T) {
	var p Presets
	p.AddID("build")
	if err := p.Add("default", "build", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := p.Resolve("default")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "build" {
		t.Fatalf("Resolve = %q, want %q", got, "build")
	}
}

func TestPresetsAddIDIgnoresEmpty(t *testing.T) {
	p := NewPresets("", "build", "build")
	if err := p.Add("default", "build", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := p.Add("empty", "", ""); !errors.Is(err, ErrUnknownID) {
		t.Fatalf("Add with empty alias = %v, want ErrUnknownID", err)
	}
}
