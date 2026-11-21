package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/httpapi"
)

type upFakeMessenger struct {
	chatID string
	texts  []string
}

func (m *upFakeMessenger) SendText(_ context.Context, chatID, text string) error {
	m.chatID = chatID
	m.texts = append(m.texts, text)
	return nil
}

type upFakeDecisions struct {
	requests []httpapi.ClientDecision
	err      error
	receipt  httpapi.ClientDecisionReceipt
}

func (d *upFakeDecisions) Decide(_ context.Context, req httpapi.ClientDecision) (httpapi.ClientDecisionReceipt, error) {
	d.requests = append(d.requests, req)
	if d.err != nil {
		return httpapi.ClientDecisionReceipt{}, d.err
	}
	return d.receipt, nil
}

type upFakeStatus struct {
	watches  []domain.Watch
	blockers []domain.BlockerIncident
	err      error
}

func (s upFakeStatus) LiveWatches() ([]domain.Watch, error) { return s.watches, s.err }

func (s upFakeStatus) ActiveBlockers() ([]domain.BlockerIncident, error) {
	return s.blockers, s.err
}

func upTestWatch(id string, pr int) domain.Watch {
	return domain.Watch{
		ID: id, PR: pr, Status: domain.StatusWatching, Verdict: domain.VerdictRetry, Revision: 4,
		Subscribers: []domain.Subscriber{{ID: "ses_1", Role: domain.RoleExecutor, Phase: domain.WorkerPhaseInWork}},
	}
}

func TestHandleUpCommandWriteVerbsUseHumanDecidePort(t *testing.T) {
	cases := []struct {
		text   string
		verb   string
		target string
		note   string
	}{
		{"/approve watch-1 спасибо", "approve", "watch-1", "спасибо"},
		{"/unblock blocker-9", "unblock", "blocker-9", ""},
		{"/pause watch-2", "pause", "watch-2", ""},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			messenger := &upFakeMessenger{}
			decisions := &upFakeDecisions{receipt: httpapi.ClientDecisionReceipt{OK: true, Applied: true}}
			handleUpCommand(context.Background(), messenger, "42", tc.text,
				upFakeStatus{}, decisions, nil)

			if len(decisions.requests) != 1 {
				t.Fatalf("decisions = %d, want 1", len(decisions.requests))
			}
			req := decisions.requests[0]
			if req.Verb != tc.verb || req.Target != tc.target || req.Note != tc.note {
				t.Fatalf("decision = %+v, want verb=%q target=%q note=%q", req, tc.verb, tc.target, tc.note)
			}
			if messenger.chatID != "42" || len(messenger.texts) != 1 ||
				!strings.Contains(messenger.texts[0], tc.verb+" "+tc.target) {
				t.Fatalf("reply = %+v, want a %s receipt", messenger, tc.verb)
			}
		})
	}
}

func TestHandleUpCommandMissingTargetDoesNotDecide(t *testing.T) {
	messenger := &upFakeMessenger{}
	decisions := &upFakeDecisions{}
	handleUpCommand(context.Background(), messenger, "42", "/approve", upFakeStatus{}, decisions, nil)

	if len(decisions.requests) != 0 {
		t.Fatalf("decisions = %d, want 0 without a target", len(decisions.requests))
	}
	if len(messenger.texts) != 1 || !strings.Contains(messenger.texts[0], "нужен") {
		t.Fatalf("reply = %v, want a target hint", messenger.texts)
	}
}

func TestHandleUpCommandIgnoresPlainChat(t *testing.T) {
	messenger := &upFakeMessenger{}
	decisions := &upFakeDecisions{}
	handleUpCommand(context.Background(), messenger, "42", "привет", upFakeStatus{}, decisions, nil)

	if len(decisions.requests) != 0 || len(messenger.texts) != 0 {
		t.Fatalf("plain chat must not dispatch: decisions=%d replies=%d", len(decisions.requests), len(messenger.texts))
	}
}

func TestHandleUpCommandStatusReportsCounts(t *testing.T) {
	blocked := upTestWatch("watch-2", 8)
	blocked.Blocked = &domain.BlockedInfo{Reason: domain.BlockReasonNotMine}
	status := upFakeStatus{
		watches:  []domain.Watch{upTestWatch("watch-1", 7), blocked},
		blockers: []domain.BlockerIncident{{ID: "blocker-1"}},
	}
	messenger := &upFakeMessenger{}
	handleUpCommand(context.Background(), messenger, "42", "/status", status, &upFakeDecisions{}, nil)

	if len(messenger.texts) != 1 {
		t.Fatalf("replies = %d, want 1", len(messenger.texts))
	}
	reply := messenger.texts[0]
	for _, want := range []string{"watches=2", "blockers=1", "attention=1", "retry=2"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply = %q, want %q", reply, want)
		}
	}
}

func TestHandleUpCommandInspectFindsWatchByPR(t *testing.T) {
	blocked := upTestWatch("watch-2", 8)
	blocked.Blocked = &domain.BlockedInfo{Reason: domain.BlockReasonNotMine, FixAttempts: 2}
	status := upFakeStatus{watches: []domain.Watch{upTestWatch("watch-1", 7), blocked}}
	messenger := &upFakeMessenger{}
	handleUpCommand(context.Background(), messenger, "42", "/inspect 8", status, &upFakeDecisions{}, nil)

	if len(messenger.texts) != 1 {
		t.Fatalf("replies = %d, want 1", len(messenger.texts))
	}
	reply := messenger.texts[0]
	for _, want := range []string{"watch=watch-2", "pr=8", "not mine", "fixAttempts=2", "subscriber=ses_1"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply = %q, want %q", reply, want)
		}
	}
	if strings.Contains(reply, "watch-1") {
		t.Fatalf("reply = %q, want only the matching watch", reply)
	}
}

func TestHandleUpCommandReportsDecisionFailure(t *testing.T) {
	messenger := &upFakeMessenger{}
	decisions := &upFakeDecisions{err: errors.New("store down")}
	handleUpCommand(context.Background(), messenger, "42", "/approve watch-1", upFakeStatus{}, decisions, nil)

	if len(messenger.texts) != 1 || !strings.Contains(messenger.texts[0], "store down") {
		t.Fatalf("reply = %v, want the decision error", messenger.texts)
	}
}

type upFailingDecider struct{}

func (upFailingDecider) Decide(context.Context, httpapi.ClientDecision) (httpapi.ClientDecisionReceipt, error) {
	return httpapi.ClientDecisionReceipt{}, errors.New("protocol mismatch")
}

type upRecordingStore struct {
	requests []httpapi.ClientDecision
}

func (s *upRecordingStore) Apply(_ context.Context, req httpapi.ClientDecision) error {
	s.requests = append(s.requests, req)
	return nil
}

func TestUpDecisionAdapterFallsBackToLocalStore(t *testing.T) {
	store := &upRecordingStore{}
	adapter := upDecisionAdapter{decider: upFailingDecider{}, store: store}
	req := httpapi.ClientDecision{Verb: "approve", Target: "watch-1"}

	receipt, err := adapter.Decide(context.Background(), req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !receipt.Applied || len(store.requests) != 1 || store.requests[0] != req {
		t.Fatalf("receipt = %+v, store requests = %+v", receipt, store.requests)
	}
}
