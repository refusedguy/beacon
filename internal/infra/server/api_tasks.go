// Package server projects Beacon's background work into the /api/v1 surface.
// This file builds one unified list of jobs and cards through a read port. It
// never opens SQLite and never copies secret-bearing source fields into the
// response.
package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// TaskKind marks which side of the unified list an entry came from. The name
// is distinct from scheduler.Kind, which describes trigger modes in this package.
type TaskKind string

const (
	// TaskKindJob marks a background job, for example a watched pull request.
	TaskKindJob TaskKind = "job"
	// TaskKindCard marks a task card.
	TaskKindCard TaskKind = "card"
)

// JobRow is one background job supplied by the projection port. Source stores
// commonly carry credentials or raw payloads alongside a job. Secret and
// Payload exist here to make that explicit: the projection never copies them
// into an Entry, so they cannot reach a client (acceptance in BS.35).
type JobRow struct {
	ID      string
	Type    string
	Status  string
	Title   string
	Secret  string
	Payload string
}

// CardRow is one task card supplied by the projection port. As with JobRow,
// Secret and Payload stay in the source layer and are dropped on projection.
type CardRow struct {
	ID      string
	Type    string
	Status  string
	Title   string
	Phase   string
	Secret  string
	Payload string
}

// Link relates a job to a card. It carries identifiers only, so the same value
// is used on both sides of the port.
type Link struct {
	JobID  string `json:"jobID"`
	CardID string `json:"cardID"`
}

// TaskSource is the projection port. An adapter supplies the three readings
// from whatever store it owns; the server never reads SQLite itself. Each
// method returns a snapshot, so filtering never mutates the source.
type TaskSource interface {
	Jobs(ctx context.Context) ([]JobRow, error)
	Cards(ctx context.Context) ([]CardRow, error)
	Links(ctx context.Context) ([]Link, error)
}

// Entry is one row of the unified list. Its wire shape is fixed to display
// fields: identifiers, classification and a title. There is no field for a
// token, payload or file body, and the projection fills only these fields.
type Entry struct {
	Kind   TaskKind `json:"kind"`
	ID     string   `json:"id"`
	Type   string   `json:"type"`
	Status string   `json:"status"`
	Title  string   `json:"title,omitempty"`
	Phase  string   `json:"phase,omitempty"`
}

// TaskList is the projected response: one unified list and the links that
// survive the same filter. Both slices are non-nil so an empty result encodes
// as [] rather than null.
type TaskList struct {
	Entries []Entry `json:"entries"`
	Links   []Link  `json:"links"`
}

// TaskFilter narrows the unified list. An empty Type or Status means no
// constraint on that field. Values are matched exactly after trimming.
type TaskFilter struct {
	Type   string
	Status string
}

// TaskProjection turns source rows into the unified API list.
type TaskProjection struct {
	source TaskSource
}

// NewTaskProjection builds a projection over source. A nil source is kept so
// List can report an error instead of panicking on bad wiring.
func NewTaskProjection(source TaskSource) *TaskProjection {
	return &TaskProjection{source: source}
}

// List reads the three projections, applies filter to jobs and cards, and
// returns one sorted list. A link is kept only when both endpoints survive
// the filter, so the response never points at an entry it does not contain.
func (p *TaskProjection) List(ctx context.Context, filter TaskFilter) (TaskList, error) {
	if p == nil || p.source == nil {
		return TaskList{}, errors.New("server: nil task source")
	}
	jobs, err := p.source.Jobs(ctx)
	if err != nil {
		return TaskList{}, fmt.Errorf("server: read job projection: %w", err)
	}
	cards, err := p.source.Cards(ctx)
	if err != nil {
		return TaskList{}, fmt.Errorf("server: read card projection: %w", err)
	}
	links, err := p.source.Links(ctx)
	if err != nil {
		return TaskList{}, fmt.Errorf("server: read link projection: %w", err)
	}

	filter = filter.normalize()
	entries := make([]Entry, 0, len(jobs)+len(cards))
	jobIDs := make(map[string]bool, len(jobs))
	cardIDs := make(map[string]bool, len(cards))
	for _, job := range jobs {
		if !filter.matches(job.Type, job.Status) {
			continue
		}
		entries = append(entries, Entry{
			Kind:   TaskKindJob,
			ID:     job.ID,
			Type:   job.Type,
			Status: job.Status,
			Title:  job.Title,
		})
		jobIDs[job.ID] = true
	}
	for _, card := range cards {
		if !filter.matches(card.Type, card.Status) {
			continue
		}
		entries = append(entries, Entry{
			Kind:   TaskKindCard,
			ID:     card.ID,
			Type:   card.Type,
			Status: card.Status,
			Title:  card.Title,
			Phase:  card.Phase,
		})
		cardIDs[card.ID] = true
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].ID < entries[j].ID
	})

	outLinks := make([]Link, 0, len(links))
	for _, link := range links {
		if jobIDs[link.JobID] && cardIDs[link.CardID] {
			outLinks = append(outLinks, link)
		}
	}
	sort.Slice(outLinks, func(i, j int) bool {
		if outLinks[i].JobID != outLinks[j].JobID {
			return outLinks[i].JobID < outLinks[j].JobID
		}
		return outLinks[i].CardID < outLinks[j].CardID
	})
	return TaskList{Entries: entries, Links: outLinks}, nil
}

func (f TaskFilter) normalize() TaskFilter {
	f.Type = strings.TrimSpace(f.Type)
	f.Status = strings.TrimSpace(f.Status)
	return f
}

func (f TaskFilter) matches(typeName, status string) bool {
	if f.Type != "" && f.Type != typeName {
		return false
	}
	if f.Status != "" && f.Status != status {
		return false
	}
	return true
}
