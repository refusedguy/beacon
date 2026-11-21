package main

import (
	"bytes"
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

	onSelection func(call int)

	terminateErr   error
	saveErr        error
	terminateCalls int
	saveCalls      int
	calls          []string
	onTerminate    func()
	onSave         func()
}

func (m *stopMockStore) Selection(string) (registryStopSelection, bool, error) {
	m.selectionCalls++
	if m.onSelection != nil {
		m.onSelection(m.selectionCalls)
	}
	return m.selection, m.found, m.selectionErr
}

func (m *stopMockStore) ActiveObligations(sessionID string) (int, error) {
	m.activeCalls++
	m.activeSession = sessionID
	return m.active, m.activeErr
}

func (m *stopMockStore) Terminate(registryStopSelection) error {
	m.terminateCalls++
	m.calls = append(m.calls, "terminate")
	if m.onTerminate != nil {
		m.onTerminate()
	}
	return m.terminateErr
}

func (m *stopMockStore) SaveState(registryStopSelection) error {
	m.saveCalls++
	m.calls = append(m.calls, "save")
	if m.onSave != nil {
		m.onSave()
	}
	return m.saveErr
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

func TestStopRegistryExecutorSucceedsAfterStateWrite(t *testing.T) {
	storeMock, want := matchingStopMock()

	got, err := stopRegistryExecutor(storeMock, storeMock, want)
	if err != nil {
		t.Fatalf("stopRegistryExecutor: %v", err)
	}
	if got.Outcome != stopOutcomeStopped {
		t.Fatalf("outcome = %q, want %q", got.Outcome, stopOutcomeStopped)
	}
	if got.StopPhase != "" || got.Reason != "" || got.Detail != "" {
		t.Fatalf("clean stop carries phase %q reason %q detail %q", got.StopPhase, got.Reason, got.Detail)
	}
	if got.Selection != want {
		t.Fatalf("selection = %+v, want %+v", got.Selection, want)
	}
	if strings.Join(storeMock.calls, ",") != "terminate,save" {
		t.Fatalf("effect order = %v, want terminate,save", storeMock.calls)
	}
}

func TestStopRegistryExecutorReportsUncertain(t *testing.T) {
	cases := []struct {
		name         string
		terminateErr error
		saveErr      error
		wantPhase    string
		wantReason   string
		wantDetail   string
		wantSaves    int
	}{
		{
			name:         "terminate error",
			terminateErr: errors.New("signal rejected"),
			wantPhase:    stopPhaseTerminate,
			wantReason:   stopReasonTerminateFailed,
			wantDetail:   "signal rejected",
		},
		{
			name:       "state write error",
			saveErr:    errors.New("disk full"),
			wantPhase:  stopPhaseStateWrite,
			wantReason: stopReasonStateWriteFailed,
			wantDetail: "disk full",
			wantSaves:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storeMock, want := matchingStopMock()
			storeMock.terminateErr = tc.terminateErr
			storeMock.saveErr = tc.saveErr

			got, err := stopRegistryExecutor(storeMock, storeMock, want)
			if err != nil {
				t.Fatalf("stopRegistryExecutor: %v", err)
			}
			if got.Outcome != stopOutcomeUncertain {
				t.Fatalf("outcome = %q, want %q", got.Outcome, stopOutcomeUncertain)
			}
			if got.StopPhase != tc.wantPhase {
				t.Fatalf("phase = %q, want %q", got.StopPhase, tc.wantPhase)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if !strings.Contains(got.Detail, tc.wantDetail) {
				t.Fatalf("detail = %q, want %q", got.Detail, tc.wantDetail)
			}
			if storeMock.terminateCalls != 1 || storeMock.saveCalls != tc.wantSaves {
				t.Fatalf("effects = terminate %d, save %d; want 1, %d",
					storeMock.terminateCalls, storeMock.saveCalls, tc.wantSaves)
			}
		})
	}
}

func TestStopRegistryExecutorRepeatsPrecondition(t *testing.T) {
	storeMock, want := matchingStopMock()
	storeMock.saveErr = errors.New("disk full")

	first, err := stopRegistryExecutor(storeMock, storeMock, want)
	if err != nil || first.Outcome != stopOutcomeUncertain {
		t.Fatalf("first stop = %+v, %v; want uncertain", first, err)
	}

	storeMock.onSelection = func(call int) {
		if call > 1 {
			storeMock.selection.Revision++
		}
	}
	second, err := stopRegistryExecutor(storeMock, storeMock, want)
	if err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if second.Outcome != stopOutcomeRefused || second.Reason != stopRefusalSelectionChanged {
		t.Fatalf("second stop = %+v, want selection_changed refusal", second)
	}
	if storeMock.selectionCalls != 2 {
		t.Fatalf("precondition reads = %d, want 2", storeMock.selectionCalls)
	}
	if storeMock.terminateCalls != 1 || storeMock.saveCalls != 1 {
		t.Fatalf("a refusal repeated an effect: terminate %d, save %d",
			storeMock.terminateCalls, storeMock.saveCalls)
	}
}

func runStopWithMock(t *testing.T, storeMock *stopMockStore) (int, string, string) {
	t.Helper()
	old := registryStopOpenStore
	registryStopOpenStore = func() (registryStopStore, func() error, error) {
		return storeMock, func() error { return nil }, nil
	}
	t.Cleanup(func() { registryStopOpenStore = old })

	var out, errOut bytes.Buffer
	c := &CLI{out: &out, errOut: &errOut}
	args := []string{"stop", "--watch", "watch-1", "--revision", "7", "--session", "ses-1", "--token", "42"}
	return runRegistry(c, args), out.String(), errOut.String()
}

func TestRegistryStopPrintsSuccessAfterStateWrite(t *testing.T) {
	storeMock, _ := matchingStopMock()
	var out bytes.Buffer
	storeMock.onSave = func() { out.WriteString("state_saved\n") }

	old := registryStopOpenStore
	registryStopOpenStore = func() (registryStopStore, func() error, error) {
		return storeMock, func() error { return nil }, nil
	}
	t.Cleanup(func() { registryStopOpenStore = old })

	c := &CLI{out: &out, errOut: &bytes.Buffer{}}
	args := []string{"stop", "--watch", "watch-1", "--revision", "7", "--session", "ses-1", "--token", "42"}
	if code := runRegistry(c, args); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	output := out.String()
	if !strings.Contains(output, "stopped watch=watch-1 revision=7 session=ses-1") {
		t.Fatalf("stdout = %q, want the stopped line", output)
	}
	saved := strings.Index(output, "state_saved")
	stopped := strings.Index(output, "stopped watch=")
	if saved < 0 || stopped < 0 || saved >= stopped {
		t.Fatalf("stdout = %q, want the state write before success", output)
	}
}

func TestRegistryStopPrintsUncertainWithoutSuccess(t *testing.T) {
	storeMock, _ := matchingStopMock()
	storeMock.saveErr = errors.New("disk full")

	code, output, _ := runStopWithMock(t, storeMock)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(output, "stop_uncertain stopPhase=state_write reason=state_write_failed") {
		t.Fatalf("stdout = %q, want the uncertain verdict", output)
	}
	if !strings.Contains(output, "disk full") {
		t.Fatalf("stdout = %q, want the error detail", output)
	}
	if strings.Contains(output, "stopped watch=") || strings.Contains(output, "stop_allowed") {
		t.Fatalf("stdout = %q, uncertain stop printed success", output)
	}
}
