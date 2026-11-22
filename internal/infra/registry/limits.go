// Package registry validates the author-owned agent registry configuration.
// This file owns the limits block (T5.17). Every quota must be an integer in
// its documented range. Negative values fail with the field name in the error,
// and omitted keys take the documented defaults.
package registry

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	defaultMaxConcurrentAgents = 4
	defaultMaxRetries          = 3
	defaultMaxTokensPerTask    = 1_000_000
	defaultMaxWakesPerDay      = 100
	defaultTimeoutSeconds      = 300
)

// Parallel slot budget exposed by the orchestrator workload report. The
// registry's max_concurrent_agents limit keeps its broad range for the agent
// registry itself; the slot budget is the narrower runtime capacity and shares
// the same config key. Zero disables capacity, like domain.NewOrchestrator.
const (
	SlotBudgetDisabled = 0
	SlotBudgetMin      = 12
	SlotBudgetMax      = 17

	// DefaultSlotBudget applies when the config does not set a budget at all.
	DefaultSlotBudget = SlotBudgetMin
)

// Limits is the validated limits block of one agent registry config.
// A value read by ParseLimits is never zero unless zero is in that field's
// range, because absent keys take the defaults in limitSpecs.
type Limits struct {
	MaxConcurrentAgents int `json:"max_concurrent_agents"`
	MaxRetries          int `json:"max_retries"`
	MaxTokensPerTask    int `json:"max_tokens_per_task"`
	MaxWakesPerDay      int `json:"max_wakes_per_day"`
	TimeoutSeconds      int `json:"timeout_seconds"`
}

// limitSpec is one documented quota: the config key, its inclusive range, the
// value used when the key is absent, and the field it writes. The ranges cap
// how much work one registered agent may ask for.
type limitSpec struct {
	field  string
	min    int64
	max    int64
	def    int64
	target func(*Limits) *int
}

// limitSpecs is the complete limits block in field order, so validation
// reports the same first failure for the same config.
var limitSpecs = []limitSpec{
	{
		field:  "max_concurrent_agents",
		min:    0,
		max:    256,
		def:    defaultMaxConcurrentAgents,
		target: func(l *Limits) *int { return &l.MaxConcurrentAgents },
	},
	{
		field:  "max_retries",
		min:    0,
		max:    10,
		def:    defaultMaxRetries,
		target: func(l *Limits) *int { return &l.MaxRetries },
	},
	{
		field:  "max_tokens_per_task",
		min:    0,
		max:    10_000_000,
		def:    defaultMaxTokensPerTask,
		target: func(l *Limits) *int { return &l.MaxTokensPerTask },
	},
	{
		field:  "max_wakes_per_day",
		min:    0,
		max:    10_000,
		def:    defaultMaxWakesPerDay,
		target: func(l *Limits) *int { return &l.MaxWakesPerDay },
	},
	{
		field:  "timeout_seconds",
		min:    1,
		max:    86_400,
		def:    defaultTimeoutSeconds,
		target: func(l *Limits) *int { return &l.TimeoutSeconds },
	},
}

// DefaultLimits returns the documented defaults of the limits block. Callers
// that build a Limits by hand can start here and override individual fields.
func DefaultLimits() Limits {
	return Limits{
		MaxConcurrentAgents: defaultMaxConcurrentAgents,
		MaxRetries:          defaultMaxRetries,
		MaxTokensPerTask:    defaultMaxTokensPerTask,
		MaxWakesPerDay:      defaultMaxWakesPerDay,
		TimeoutSeconds:      defaultTimeoutSeconds,
	}
}

// ValidateLimits checks an assembled Limits value. It reports the first bad
// field by name and does not fill defaults; ParseLimits does that for raw
// config data.
func ValidateLimits(limits Limits) error {
	for _, spec := range limitSpecs {
		value := int64(*spec.target(&limits))
		if value < 0 {
			return limitError(spec.field, "must be nonnegative", value)
		}
		if value < spec.min || value > spec.max {
			return limitError(spec.field, fmt.Sprintf("must be in range [%d, %d]", spec.min, spec.max), value)
		}
	}
	return nil
}

// ParseLimits validates a raw limits block from the YAML or JSON parser and
// fills absent keys with the documented defaults. Numbers arrive as int,
// int64, json.Number or float64 depending on the decoder; each must hold an
// integer. A fractional number, a string, an unknown key, a negative value or
// a value outside its range fails with an error naming the field.
func ParseLimits(raw map[string]any) (Limits, error) {
	parsed := DefaultLimits()
	if len(raw) == 0 {
		return parsed, nil
	}

	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !knownLimitField(key) {
			return Limits{}, limitError(key, "unknown field", nil)
		}
	}

	for _, spec := range limitSpecs {
		rawValue, ok := raw[spec.field]
		if !ok {
			continue
		}
		value, err := limitInt(rawValue)
		if err != nil {
			return Limits{}, limitError(spec.field, "must be an integer", rawValue)
		}
		if value < 0 {
			return Limits{}, limitError(spec.field, "must be nonnegative", value)
		}
		if value < spec.min || value > spec.max {
			return Limits{}, limitError(spec.field, fmt.Sprintf("must be in range [%d, %d]", spec.min, spec.max), value)
		}
		*spec.target(&parsed) = int(value)
	}
	return parsed, nil
}

// ValidateSlotBudget checks one parallel-slot budget. Zero is a valid "no
// capacity" configuration; every positive value must sit in [12, 17]. A bad
// value reports the config key so the caller can reject the configuration.
func ValidateSlotBudget(budget int) error {
	if budget == SlotBudgetDisabled {
		return nil
	}
	if budget < SlotBudgetMin || budget > SlotBudgetMax {
		return limitError("max_concurrent_agents",
			fmt.Sprintf("must be %d or in range [%d, %d]", SlotBudgetDisabled, SlotBudgetMin, SlotBudgetMax),
			budget)
	}
	return nil
}

// ParseSlotBudgetValue validates one raw max_concurrent_agents value. Other
// keys in the limits block are ignored, so a config with unrelated limits does
// not break the slot budget reader.
func ParseSlotBudgetValue(raw any) (int, error) {
	value, err := limitInt(raw)
	if err != nil {
		return 0, limitError("max_concurrent_agents", "must be an integer", raw)
	}
	budget := int(value)
	if err := ValidateSlotBudget(budget); err != nil {
		return 0, err
	}
	return budget, nil
}

// ResolveSlotBudget returns the effective budget for an optional configured
// value. A nil pointer means the config has no budget, so the documented
// default applies. A configured value is validated before it is returned.
func ResolveSlotBudget(configured *int) (int, error) {
	if configured == nil {
		return DefaultSlotBudget, nil
	}
	if err := ValidateSlotBudget(*configured); err != nil {
		return 0, err
	}
	return *configured, nil
}

func knownLimitField(field string) bool {
	for _, spec := range limitSpecs {
		if spec.field == field {
			return true
		}
	}
	return false
}

// limitInt accepts only whole numbers. float64 is checked before the cast
// because a fractional value would otherwise truncate and pass validation.
func limitInt(value any) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint:
		if uint64(v) > math.MaxInt64 {
			return 0, fmt.Errorf("integer overflows int64")
		}
		return int64(v), nil
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint64:
		if v > math.MaxInt64 {
			return 0, fmt.Errorf("integer overflows int64")
		}
		return int64(v), nil
	case json.Number:
		return v.Int64()
	case float64:
		const bound = float64(1 << 63)
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || v >= bound || v <= -bound {
			return 0, fmt.Errorf("not an int64")
		}
		return int64(v), nil
	default:
		return 0, fmt.Errorf("not an integer")
	}
}

func limitError(field, reason string, value any) error {
	message := fmt.Sprintf("limits.%s: %s", field, reason)
	if value != nil {
		message = fmt.Sprintf("%s, got %v", message, value)
	}
	return domain.NewAppError(domain.CodeInvalidInput, message)
}
