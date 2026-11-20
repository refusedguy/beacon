package registry

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

func assertLimitError(t *testing.T, err error, field, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error naming field %s", field)
	}
	var app *domain.AppError
	if !errors.As(err, &app) {
		t.Fatalf("error type = %T, want *domain.AppError", err)
	}
	if app.Code != domain.CodeInvalidInput {
		t.Fatalf("error code = %s, want %s", app.Code, domain.CodeInvalidInput)
	}
	if want := "limits." + field; !strings.Contains(app.Message, want) {
		t.Fatalf("error message %q does not name field %q", app.Message, want)
	}
	if reason != "" && !strings.Contains(app.Message, reason) {
		t.Fatalf("error message %q does not explain %q", app.Message, reason)
	}
}

func TestParseLimitsRejectsNegativeByFieldName(t *testing.T) {
	fields := []string{
		"max_concurrent_agents",
		"max_retries",
		"max_tokens_per_task",
		"max_wakes_per_day",
		"timeout_seconds",
	}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			_, err := ParseLimits(map[string]any{field: -1})
			assertLimitError(t, err, field, "must be nonnegative")
		})
	}
}

func TestValidateLimitsRejectsNegativeByFieldName(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxWakesPerDay = -5
	assertLimitError(t, ValidateLimits(limits), "max_wakes_per_day", "must be nonnegative")
}

func TestParseLimitsRejectsNonIntegers(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"fraction", 2.5},
		{"string", "3"},
		{"bool", true},
		{"null", nil},
		{"json fraction", json.Number("2.5")},
		{"non numeric json", json.Number("x")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseLimits(map[string]any{"max_retries": tc.value})
			assertLimitError(t, err, "max_retries", "must be an integer")
		})
	}
}

func TestParseLimitsRejectsOutOfRange(t *testing.T) {
	cases := []struct {
		field string
		value any
	}{
		{"max_concurrent_agents", 257},
		{"max_retries", 11},
		{"max_tokens_per_task", 10_000_001},
		{"max_wakes_per_day", 10_001},
		{"timeout_seconds", 0},
		{"timeout_seconds", 86_401},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			_, err := ParseLimits(map[string]any{tc.field: tc.value})
			assertLimitError(t, err, tc.field, "must be in range")
		})
	}
}

func TestParseLimitsRejectsUnknownField(t *testing.T) {
	_, err := ParseLimits(map[string]any{"max_wakez_per_day": 5})
	assertLimitError(t, err, "max_wakez_per_day", "unknown field")
}

func TestParseLimitsFillsDocumentedDefaults(t *testing.T) {
	want := Limits{
		MaxConcurrentAgents: 4,
		MaxRetries:          3,
		MaxTokensPerTask:    1_000_000,
		MaxWakesPerDay:      100,
		TimeoutSeconds:      300,
	}
	if got := DefaultLimits(); got != want {
		t.Fatalf("DefaultLimits() = %+v, want %+v", got, want)
	}
	got, err := ParseLimits(nil)
	if err != nil {
		t.Fatalf("ParseLimits(nil): %v", err)
	}
	if got != want {
		t.Fatalf("ParseLimits(nil) = %+v, want %+v", got, want)
	}
}

func TestParseLimitsAcceptsWholeNumbers(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"int", 7},
		{"int64", int64(7)},
		{"uint", uint(7)},
		{"json number", json.Number("7")},
		{"whole float from json", 7.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLimits(map[string]any{"max_retries": tc.value})
			if err != nil {
				t.Fatalf("ParseLimits: %v", err)
			}
			if got.MaxRetries != 7 {
				t.Fatalf("MaxRetries = %d, want 7", got.MaxRetries)
			}
			if got.MaxWakesPerDay != defaultMaxWakesPerDay {
				t.Fatalf("MaxWakesPerDay = %d, want default %d", got.MaxWakesPerDay, defaultMaxWakesPerDay)
			}
		})
	}
}

func TestParseLimitsAcceptsRangeBoundaries(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
	}{
		{
			name: "lower",
			raw: map[string]any{
				"max_concurrent_agents": 0,
				"max_retries":           0,
				"max_tokens_per_task":   0,
				"max_wakes_per_day":     0,
				"timeout_seconds":       1,
			},
		},
		{
			name: "upper",
			raw: map[string]any{
				"max_concurrent_agents": 256,
				"max_retries":           10,
				"max_tokens_per_task":   10_000_000,
				"max_wakes_per_day":     10_000,
				"timeout_seconds":       86_400,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseLimits(tc.raw); err != nil {
				t.Fatalf("ParseLimits: %v", err)
			}
		})
	}
}

func TestValidateLimitsAcceptsDefaults(t *testing.T) {
	if err := ValidateLimits(DefaultLimits()); err != nil {
		t.Fatalf("ValidateLimits(DefaultLimits()): %v", err)
	}
}
