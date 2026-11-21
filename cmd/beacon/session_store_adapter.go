package main

import (
	"time"

	"github.com/refusedguy/beacon/internal/app/session"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// sessionDBAdapter adapts the SQLite-backed store to the app-layer
// session.SessionStore port. The conversion stays in cmd so app names no
// infra type (ARDH-15 DIP).
type sessionDBAdapter struct {
	db *store.DB
}

var (
	_ session.SessionStore      = (*sessionDBAdapter)(nil)
	_ session.TaskSessionFinder = (*sessionDBAdapter)(nil)
)

func newSessionDBAdapter(db *store.DB) *sessionDBAdapter {
	return &sessionDBAdapter{db: db}
}

func toStoreSession(s session.Session) store.Session {
	return store.Session{
		ID:        s.ID,
		Status:    s.Status,
		CreatedAt: s.CreatedAt,
		LastSeen:  s.LastSeen,
		Metadata:  s.Metadata,
	}
}

func toAppSession(s store.Session) session.Session {
	return session.Session{
		ID:        s.ID,
		Status:    s.Status,
		CreatedAt: s.CreatedAt,
		LastSeen:  s.LastSeen,
		Metadata:  s.Metadata,
	}
}

func (a *sessionDBAdapter) SaveSession(s session.Session) error {
	return a.db.SaveSession(toStoreSession(s))
}

func (a *sessionDBAdapter) GetSession(id string) (session.Session, bool, error) {
	s, found, err := a.db.GetSession(id)
	return toAppSession(s), found, err
}

func (a *sessionDBAdapter) Touch(id string, at time.Time) error {
	return a.db.Touch(id, at)
}

func (a *sessionDBAdapter) FindSessionByTask(task string) (session.Session, bool, error) {
	s, found, err := a.db.FindSessionByTask(task)
	return toAppSession(s), found, err
}
