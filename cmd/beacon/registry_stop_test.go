package main

import (
	"errors"
	"strings"
	"testing"
)

// stopMockStore is the mock infra port. It records which reads the check made
// so a refusal can be proven to stop before the obligation read.
type stopMockStore struct {
	selection    registryStopSelection
	found        bool
	selectionErr error
	active       int
	activeErr    error

	selectionCalls int
	activeCalls    int
	activeSession  string
}

func (m *stopMockStore) Selection(string) (registryStopSelection, bool, error) {
	m.selectionCalls++
	return m.selection, m.found, m.selectionErr
}

func (m *stopMockStore) ActiveObligations(sessionID string) (int, error) {
	m.activeCalls++
	m.activeSession = sessionID
	return m.active, m.activeErr
}

func matchingStopMock() (*stopMockStore, registryStopSelection) {
	want := registryStopSelection{
		WatchID:   "watch-1",
		Revision:  7,
		SessionID: "ses-1",
		Token:     "42",
	}
	return &stopMockStore{selection: want, found: true}, want
}

func TestCheckRegistryStopAllowsMatchingSelection(t *testing.T) {
	storeMock, want := matchingStopMock()

	got, err := checkRegistryStop(storeMock, want)
	if err != nil {
		t.Fatalf("checkRegistryStop: %v", err)
	}
	if !got.Allowed {
		t.Fatalf("verdict = %+v, want allowed", got)
	}
	if got.Reason != "" || got.Detail != "" {
		t.Fatalf("allowed verdict carries reason %q detail %q", got.Reason, got.Detail)
	}
	if storeMock.selectionCalls != 1 || storeMock.activeCalls != 1 {
		t.Fatalf("reads = selection %d, obligations %d; want 1, 1",
			storeMock.selectionCalls, storeMock.activeCalls)
	}
	if storeMock.activeSession != want.SessionID {
		t.Fatalf("obligations session = %q, want %q", storeMock.activeSession, want.SessionID)
	}
}

func TestCheckRegistryStopRefusesChangedSelection(t *testing.T) {
	cases := []struct {
		name   string
		change func(*registryStopSelection)
		field  string
	}{
		{"watch id", func(s *registryStopSelection) { s.WatchID = "watch-2" }, "watchID"},
		{"revision", func(s *registryStopSelection) { s.Revision++ }, "revision"},
		{"session id", func(s *registryStopSelection) { s.SessionID = "ses-2" }, "sessionID"},
		{"token", func(s *registryStopSelection) { s.Token = "43" }, "token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storeMock, want := matchingStopMock()
			tc.change(&storeMock.selection)

			got, err := checkRegistryStop(storeMock, want)
			if err != nil {
				t.Fatalf("checkRegistryStop: %v", err)
			}
			if got.Allowed {
				t.Fatalf("verdict = %+v, want refusal", got)
			}
			if got.Reason != stopRefusalSelectionChanged {
				t.Fatalf("reason = %q, want %q", got.Reason, stopRefusalSelectionChanged)
			}
			if !strings.Contains(got.Detail, tc.field) {
				t.Fatalf("detail %q does not name %q", got.Detail, tc.field)
			}
			if storeMock.activeCalls != 0 {
				t.Fatalf("obligations read %d times on a changed selection, want 0", storeMock.activeCalls)
			}
		})
	}
}

func TestCheckRegistryStopRefusesMissingBinding(t *testing.T) {
	storeMock, want := matchingStopMock()
	storeMock.found = false

	got, err := checkRegistryStop(storeMock, want)
	if err != nil {
		t.Fatalf("checkRegistryStop: %v", err)
	}
	if got.Allowed || got.Reason != stopRefusalSelectionChanged {
		t.Fatalf("verdict = %+v, want selection_changed", got)
	}
	if storeMock.activeCalls != 0 {
		t.Fatalf("obligations read %d times without a binding, want 0", storeMock.activeCalls)
	}
}

func TestCheckRegistryStopRefusesActiveObligations(t *testing.T) {
	storeMock, want := matchingStopMock()
	storeMock.active = 2

	got, err := checkRegistryStop(storeMock, want)
	if err != nil {
		t.Fatalf("checkRegistryStop: %v", err)
	}
	if got.Allowed {
		t.Fatalf("verdict = %+v, want refusal", got)
	}
	if got.Reason != stopRefusalActiveObligations {
		t.Fatalf("reason = %q, want %q", got.Reason, stopRefusalActiveObligations)
	}
	if !strings.Contains(got.Detail, "2") || !strings.Contains(got.Detail, want.SessionID) {
		t.Fatalf("detail %q does not name the count and session", got.Detail)
	}
}

func TestCheckRegistryStopPropagatesReadErrors(t *testing.T) {
	wantErr := errors.New("sqlite unavailable")

	t.Run("selection", func(t *testing.T) {
		storeMock, want := matchingStopMock()
		storeMock.selectionErr = wantErr

		if _, err := checkRegistryStop(storeMock, want); !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
		if storeMock.activeCalls != 0 {
			t.Fatalf("obligations read after selection error, want 0")
		}
	})

	t.Run("obligations", func(t *testing.T) {
		storeMock, want := matchingStopMock()
		storeMock.activeErr = wantErr

		if _, err := checkRegistryStop(storeMock, want); !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
	})
}

func TestCheckRegistryStopRequiresExpectedValues(t *testing.T) {
	valid := registryStopSelection{WatchID: "watch-1", Revision: 7, SessionID: "ses-1", Token: "42"}
	cases := []struct {
		name string
		want registryStopSelection
	}{
		{"no watch id", func() registryStopSelection { s := valid; s.WatchID = " "; return s }()},
		{"no revision", func() registryStopSelection { s := valid; s.Revision = 0; return s }()},
		{"no session id", func() registryStopSelection { s := valid; s.SessionID = ""; return s }()},
		{"no token", func() registryStopSelection { s := valid; s.Token = ""; return s }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storeMock, _ := matchingStopMock()
			if _, err := checkRegistryStop(storeMock, tc.want); err == nil {
				t.Fatalf("checkRegistryStop accepted %+v", tc.want)
			}
			if storeMock.selectionCalls != 0 || storeMock.activeCalls != 0 {
				t.Fatalf("store was read for an incomplete precondition")
			}
		})
	}
}

func TestCheckRegistryStopRejectsNilStore(t *testing.T) {
	_, want := matchingStopMock()
	if _, err := checkRegistryStop(nil, want); err == nil {
		t.Fatal("checkRegistryStop accepted a nil store")
	}
}

func TestSelectionDiffNamesEveryChangedField(t *testing.T) {
	want := registryStopSelection{WatchID: "watch-1", Revision: 7, SessionID: "ses-1", Token: "42"}
	current := registryStopSelection{WatchID: "watch-2", Revision: 8, SessionID: "ses-2", Token: "43"}

	diff := selectionDiff(current, want)
	if len(diff) != 4 {
		t.Fatalf("diff = %+v, want four entries", diff)
	}
	for _, field := range []string{"watchID", "revision", "sessionID", "token"} {
		found := false
		for _, line := range diff {
			if strings.Contains(line, field) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("diff %+v does not name %q", diff, field)
		}
	}
}
