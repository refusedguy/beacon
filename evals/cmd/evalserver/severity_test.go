//go:build eval

package main

import "testing"

func TestMapSecuritySeverityMatrix(t *testing.T) {
	tests := []struct {
		severity securitySeverity
		level    gateLevel
		blocking bool
	}{
		{severityCritical, levelError, true},
		{severityHigh, levelError, true},
		{severityMedium, levelWarning, false},
		{severityLow, levelWarning, false},
	}
	for _, tt := range tests {
		m, err := mapSecuritySeverity(tt.severity, severityPolicy{})
		if err != nil {
			t.Fatalf("mapSecuritySeverity(%q): unexpected error: %v", tt.severity, err)
		}
		if m.severity != tt.severity {
			t.Errorf("mapSecuritySeverity(%q): severity=%q, want %q", tt.severity, m.severity, tt.severity)
		}
		if m.level != tt.level {
			t.Errorf("mapSecuritySeverity(%q): level=%q, want %q", tt.severity, m.level, tt.level)
		}
		if m.blocking != tt.blocking {
			t.Errorf("mapSecuritySeverity(%q): blocking=%t, want %t", tt.severity, m.blocking, tt.blocking)
		}
	}
}

func TestMapSecuritySeverityRejectsUnknown(t *testing.T) {
	for _, severity := range []securitySeverity{"", "info", "critical ", "CRITICAL", "error"} {
		m, err := mapSecuritySeverity(severity, severityPolicy{})
		if err == nil {
			t.Errorf("mapSecuritySeverity(%q): expected an error for an unknown severity", severity)
		}
		if m != (mappedSeverity{}) {
			t.Errorf("mapSecuritySeverity(%q): got %+v, want zero value", severity, m)
		}
	}
}

func TestMapSecuritySeverityNoRawScaleAsLevel(t *testing.T) {
	for _, severity := range allSeverities {
		m, err := mapSecuritySeverity(severity, severityPolicy{})
		if err != nil {
			t.Fatalf("mapSecuritySeverity(%q): unexpected error: %v", severity, err)
		}
		if m.level != levelError && m.level != levelWarning {
			t.Errorf("mapSecuritySeverity(%q): level=%q, want error or warning", severity, m.level)
		}
		if m.level == gateLevel(severity) {
			t.Errorf("mapSecuritySeverity(%q): raw security scale leaked into gate level", severity)
		}
	}
}

func TestMapSecuritySeverityPolicyChangesOnlyBlocking(t *testing.T) {
	for _, severity := range []securitySeverity{severityMedium, severityLow} {
		plain, err := mapSecuritySeverity(severity, severityPolicy{})
		if err != nil {
			t.Fatalf("mapSecuritySeverity(%q) default: unexpected error: %v", severity, err)
		}
		escalated, err := mapSecuritySeverity(severity, severityPolicy{warnBlocking: true})
		if err != nil {
			t.Fatalf("mapSecuritySeverity(%q) escalated: unexpected error: %v", severity, err)
		}
		if plain.level != levelWarning || escalated.level != plain.level {
			t.Errorf("mapSecuritySeverity(%q): level changed with policy: %q -> %q", severity, plain.level, escalated.level)
		}
		if plain.blocking {
			t.Errorf("mapSecuritySeverity(%q): warning blocks by default", severity)
		}
		if !escalated.blocking {
			t.Errorf("mapSecuritySeverity(%q): warnBlocking=true did not escalate", severity)
		}
	}
}

func TestMapSecuritySeverityDeterministic(t *testing.T) {
	for _, severity := range allSeverities {
		for _, policy := range []severityPolicy{{}, {warnBlocking: true}} {
			first, errFirst := mapSecuritySeverity(severity, policy)
			second, errSecond := mapSecuritySeverity(severity, policy)
			if errFirst != errSecond || first != second {
				t.Errorf("mapSecuritySeverity(%q, %+v): %+v/%v then %+v/%v",
					severity, policy, first, errFirst, second, errSecond)
			}
		}
	}
}
