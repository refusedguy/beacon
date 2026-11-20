package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// mockTaskSource implements the projection port. Either slice can be empty and
// any read can be failed independently, so each projection path is testable.
type mockTaskSource struct {
	jobs     []JobRow
	cards    []CardRow
	links    []Link
	jobsErr  error
	cardsErr error
	linksErr error
}

func (m *mockTaskSource) Jobs(context.Context) ([]JobRow, error) {
	return m.jobs, m.jobsErr
}

func (m *mockTaskSource) Cards(context.Context) ([]CardRow, error) {
	return m.cards, m.cardsErr
}

func (m *mockTaskSource) Links(context.Context) ([]Link, error) {
	return m.links, m.linksErr
}

func sampleTaskSource() *mockTaskSource {
	return &mockTaskSource{
		jobs: []JobRow{
			{ID: "job-pr", Type: "pr", Status: "watching", Title: "Watch PR", Secret: "ghp_job", Payload: "raw-job"},
			{ID: "job-sched", Type: "schedule", Status: "done", Title: "Nightly", Secret: "ghp_sched"},
		},
		cards: []CardRow{
			{ID: "BS.35", Type: "task", Status: "in_progress", Title: "Tasks API", Phase: "P3", Secret: "card-token"},
			{ID: "BS.17", Type: "task", Status: "done", Title: "Event replay", Phase: "P2"},
		},
		links: []Link{
			{JobID: "job-pr", CardID: "BS.35"},
			{JobID: "job-sched", CardID: "BS.17"},
		},
	}
}

func TestTaskListUnified(t *testing.T) {
	got, err := NewTaskProjection(sampleTaskSource()).List(context.Background(), TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Kind: TaskKindCard, ID: "BS.17", Type: "task", Status: "done", Title: "Event replay", Phase: "P2"},
		{Kind: TaskKindCard, ID: "BS.35", Type: "task", Status: "in_progress", Title: "Tasks API", Phase: "P3"},
		{Kind: TaskKindJob, ID: "job-pr", Type: "pr", Status: "watching", Title: "Watch PR"},
		{Kind: TaskKindJob, ID: "job-sched", Type: "schedule", Status: "done", Title: "Nightly"},
	}
	if len(got.Entries) != len(want) {
		t.Fatalf("entries = %d, want %d: %+v", len(got.Entries), len(want), got.Entries)
	}
	for i := range want {
		if got.Entries[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got.Entries[i], want[i])
		}
	}
	wantLinks := []Link{{JobID: "job-pr", CardID: "BS.35"}, {JobID: "job-sched", CardID: "BS.17"}}
	if len(got.Links) != len(wantLinks) {
		t.Fatalf("links = %+v, want %+v", got.Links, wantLinks)
	}
	for i := range wantLinks {
		if got.Links[i] != wantLinks[i] {
			t.Fatalf("link %d = %+v, want %+v", i, got.Links[i], wantLinks[i])
		}
	}
}

func TestTaskListFilterByType(t *testing.T) {
	got, err := NewTaskProjection(sampleTaskSource()).List(context.Background(), TaskFilter{Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %+v, want the two task cards", got.Entries)
	}
	for _, e := range got.Entries {
		if e.Kind != TaskKindCard || e.Type != "task" {
			t.Fatalf("entry = %+v, want a task card", e)
		}
	}
	// Every link points at a job, which the type filter removed.
	if len(got.Links) != 0 {
		t.Fatalf("links = %+v, want none without their jobs", got.Links)
	}
}

func TestTaskListFilterByStatus(t *testing.T) {
	got, err := NewTaskProjection(sampleTaskSource()).List(context.Background(), TaskFilter{Status: " done "})
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"BS.17", "job-sched"}
	if len(got.Entries) != len(wantIDs) {
		t.Fatalf("entries = %+v, want %v", got.Entries, wantIDs)
	}
	for i, id := range wantIDs {
		if got.Entries[i].ID != id || got.Entries[i].Status != "done" {
			t.Fatalf("entry %d = %+v, want %s with status done", i, got.Entries[i], id)
		}
	}
	if len(got.Links) != 1 || got.Links[0] != (Link{JobID: "job-sched", CardID: "BS.17"}) {
		t.Fatalf("links = %+v, want only the done pair", got.Links)
	}
}

func TestTaskListFilterByTypeAndStatus(t *testing.T) {
	got, err := NewTaskProjection(sampleTaskSource()).List(context.Background(), TaskFilter{
		Type: "pr", Status: "watching",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].ID != "job-pr" {
		t.Fatalf("entries = %+v, want only job-pr", got.Entries)
	}
	if len(got.Links) != 0 {
		t.Fatalf("links = %+v, want none without the card", got.Links)
	}
}

func TestTaskListFilterDropsDanglingLink(t *testing.T) {
	src := sampleTaskSource()
	src.links = append(src.links, Link{JobID: "job-pr", CardID: "missing-card"})
	got, err := NewTaskProjection(src).List(context.Background(), TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range got.Links {
		if link.CardID == "missing-card" {
			t.Fatalf("link to an absent card survived: %+v", got.Links)
		}
	}
}

func TestTaskListEmpty(t *testing.T) {
	got, err := NewTaskProjection(&mockTaskSource{}).List(context.Background(), TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Entries == nil || got.Links == nil {
		t.Fatalf("empty result must use non-nil slices: %+v", got)
	}
	if len(got.Entries) != 0 || len(got.Links) != 0 {
		t.Fatalf("empty result = %+v, want no entries and no links", got)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"entries":[],"links":[]}` {
		t.Fatalf("empty JSON = %s, want [] for both slices", data)
	}
}

func TestTaskListErrors(t *testing.T) {
	sentinel := errors.New("projection read failed")
	cases := []struct {
		name   string
		mutate func(*mockTaskSource)
	}{
		{"jobs", func(m *mockTaskSource) { m.jobsErr = sentinel }},
		{"cards", func(m *mockTaskSource) { m.cardsErr = sentinel }},
		{"links", func(m *mockTaskSource) { m.linksErr = sentinel }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := sampleTaskSource()
			tc.mutate(src)
			got, err := NewTaskProjection(src).List(context.Background(), TaskFilter{})
			if !errors.Is(err, sentinel) {
				t.Fatalf("error = %v, want wrapped %v", err, sentinel)
			}
			if len(got.Entries) != 0 || len(got.Links) != 0 {
				t.Fatalf("failed read returned data: %+v", got)
			}
		})
	}
}

func TestTaskListNoSecrets(t *testing.T) {
	got, err := NewTaskProjection(sampleTaskSource()).List(context.Background(), TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"ghp_job", "ghp_sched", "card-token", "raw-job", "secret", "payload"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("projection leaked %q: %s", secret, data)
		}
	}
}

func TestTaskListNilSource(t *testing.T) {
	for _, p := range []*TaskProjection{nil, NewTaskProjection(nil)} {
		if _, err := p.List(context.Background(), TaskFilter{}); err == nil {
			t.Fatal("nil source must report an error, not panic")
		}
	}
}
