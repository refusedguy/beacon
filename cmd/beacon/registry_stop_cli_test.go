package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// registryStopCLIStore is the read-only store stand-in for the stop command.
type registryStopCLIStore struct {
	selection registryStopSelection
	found     bool
	active    int
	err       error
}

func (s registryStopCLIStore) Selection(string) (registryStopSelection, bool, error) {
	return s.selection, s.found, s.err
}

func (s registryStopCLIStore) ActiveObligations(string) (int, error) {
	return s.active, s.err
}

// runRegistryStopForTest installs a store stand-in and drives the real
// dispatch path through the registry subcommand.
func runRegistryStopForTest(t *testing.T, port registryStopStore, args []string) (int, string, string) {
	t.Helper()
	old := registryStopOpenStore
	registryStopOpenStore = func() (registryStopStore, func() error, error) {
		if port == nil {
			return nil, nil, fmt.Errorf("store unavailable")
		}
		return port, func() error { return nil }, nil
	}
	t.Cleanup(func() { registryStopOpenStore = old })

	var out, errOut bytes.Buffer
	c := &CLI{out: &out, errOut: &errOut}
	code := runRegistry(c, append([]string{"stop"}, args...))
	return code, out.String(), errOut.String()
}

func stopFlags() []string {
	return []string{"--watch", "w1", "--revision", "7", "--session", "ses1", "--token", "tok1"}
}

func TestT556RegistryUnknownSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	c := &CLI{out: &out, errOut: &errOut}
	if code := runRegistry(c, []string{"bogus"}); code != 2 {
		t.Fatalf("unknown subcommand code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "неизвестная подкоманда") {
		t.Fatalf("stderr = %q, want the unknown-subcommand message", errOut.String())
	}
}

func TestT556RegistryNoSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	c := &CLI{out: &out, errOut: &errOut}
	if code := runRegistry(c, nil); code != 2 {
		t.Fatalf("missing subcommand code = %d, want 2", code)
	}
}

func TestT556RegistryStopRequiresExpectedFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no flags", nil},
		{"missing revision", []string{"--watch", "w1", "--session", "ses1", "--token", "tok1"}},
		{"missing token", []string{"--watch", "w1", "--revision", "7", "--session", "ses1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errOut := runRegistryStopForTest(t, registryStopCLIStore{}, tc.args)
			if code != 2 {
				t.Fatalf("code = %d, want 2", code)
			}
			if !strings.Contains(errOut, "registry stop") {
				t.Fatalf("stderr = %q, want the expectation-flag error", errOut)
			}
		})
	}
}

func TestT556RegistryStopRefusesChangedSelection(t *testing.T) {
	port := registryStopCLIStore{
		found: true,
		selection: registryStopSelection{
			WatchID: "w1", Revision: 8, SessionID: "ses1", Token: "tok1",
		},
	}
	code, out, _ := runRegistryStopForTest(t, port, stopFlags())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(out, "stop_refused") || !strings.Contains(out, "selection_changed") {
		t.Fatalf("stdout = %q, want refused with selection_changed", out)
	}
}

func TestT556RegistryStopRefusesActiveObligations(t *testing.T) {
	port := registryStopCLIStore{
		found: true,
		selection: registryStopSelection{
			WatchID: "w1", Revision: 7, SessionID: "ses1", Token: "tok1",
		},
		active: 1,
	}
	code, out, _ := runRegistryStopForTest(t, port, stopFlags())
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(out, "active_obligations") {
		t.Fatalf("stdout = %q, want active_obligations reason", out)
	}
}

func TestT556RegistryStopAllowsMatchingSelection(t *testing.T) {
	port := registryStopCLIStore{
		found: true,
		selection: registryStopSelection{
			WatchID: "w1", Revision: 7, SessionID: "ses1", Token: "tok1",
		},
	}
	code, out, _ := runRegistryStopForTest(t, port, stopFlags())
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(out, "stop_allowed watch=w1 revision=7 session=ses1") {
		t.Fatalf("stdout = %q, want the allowed verdict", out)
	}
}

func TestT556RegistryStopStoreError(t *testing.T) {
	old := registryStopOpenStore
	registryStopOpenStore = func() (registryStopStore, func() error, error) {
		return nil, nil, fmt.Errorf("db down")
	}
	t.Cleanup(func() { registryStopOpenStore = old })

	var out, errOut bytes.Buffer
	c := &CLI{out: &out, errOut: &errOut}
	code := runRegistry(c, append([]string{"stop"}, stopFlags()...))
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "db down") {
		t.Fatalf("stderr = %q, want the store error", errOut.String())
	}
}
