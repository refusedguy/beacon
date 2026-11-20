// Package registry resolves named presets to configuration IDs.
//
// OpenRig keeps presets separate from configuration IDs: a preset is a name
// standing for a configuration, not the configuration ID itself. A preset
// carries an alias, which must name an existing configuration ID, and may
// recommend exactly one configuration ID. Both references are checked when the
// preset is added, so a bad preset never enters the registry.
package registry

import (
	"errors"
	"fmt"
)

var (
	// ErrUnknownPreset marks a lookup of a name that is not a registered preset.
	ErrUnknownPreset = errors.New("registry: unknown preset")
	// ErrUnknownID marks an alias or recommended value that does not name a
	// known configuration ID.
	ErrUnknownID = errors.New("registry: unknown configuration ID")
)

// Preset is one named configuration. Alias names the configuration ID the
// preset stands for and must be known when the preset is added. Recommended,
// when non-empty, names the single configuration ID the preset recommends and
// must also be known. An empty Recommended means the preset recommends none.
type Preset struct {
	Alias       string
	Recommended string
}

// Presets holds configuration IDs and the presets that reference them. The
// zero value is ready for use.
type Presets struct {
	ids     map[string]struct{}
	presets map[string]Preset
}

// NewPresets returns an empty Presets preloaded with ids. An empty id is
// ignored.
func NewPresets(ids ...string) *Presets {
	p := &Presets{}
	for _, id := range ids {
		p.AddID(id)
	}
	return p
}

// AddID registers a configuration ID that presets may reference. Empty IDs
// are ignored and adding the same ID twice is a no-op.
func (p *Presets) AddID(id string) {
	if id == "" {
		return
	}
	if p.ids == nil {
		p.ids = make(map[string]struct{})
	}
	p.ids[id] = struct{}{}
}

// hasID reports whether id is a known configuration ID.
func (p *Presets) hasID(id string) bool {
	_, ok := p.ids[id]
	return ok
}

// Add registers name as a preset. The alias must name a known configuration
// ID, and so must Recommended when it is set. On any error the registry is
// left unchanged: an invalid preset is never partly registered.
func (p *Presets) Add(name, alias, recommended string) error {
	if name == "" {
		return fmt.Errorf("registry: preset name is empty")
	}
	if !p.hasID(alias) {
		return fmt.Errorf("%w: alias %q of preset %q", ErrUnknownID, alias, name)
	}
	if recommended != "" && !p.hasID(recommended) {
		return fmt.Errorf("%w: recommended %q of preset %q", ErrUnknownID, recommended, name)
	}
	if p.presets == nil {
		p.presets = make(map[string]Preset)
	}
	p.presets[name] = Preset{Alias: alias, Recommended: recommended}
	return nil
}

// Resolve returns the configuration ID that the named preset stands for. An
// unknown name is rejected with ErrUnknownPreset, never answered with an empty
// ID.
func (p *Presets) Resolve(name string) (string, error) {
	preset, ok := p.presets[name]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownPreset, name)
	}
	return preset.Alias, nil
}

// Recommended returns the single configuration ID recommended by the named
// preset, or an empty string when the preset recommends none. An unknown name
// is rejected with ErrUnknownPreset.
func (p *Presets) Recommended(name string) (string, error) {
	preset, ok := p.presets[name]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownPreset, name)
	}
	return preset.Recommended, nil
}
