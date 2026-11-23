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

// sessionRow is the common shape of store.Session and session.Session. Both
// stay named in their own packages so app names no infra type (ARDH-15), but
// their underlying types match, so a direct conversion moves values across.
// The assertions fail at build time if either shape drifts.
type sessionRow = struct {
	ID        string
	Status    string
	CreatedAt time.Time
	LastSeen  time.Time
	Metadata  map[string]string
}

var (
	_ sessionRow                = session.Session{}
	_ sessionRow                = store.Session{}
	_ session.SessionStore      = (*sessionDBAdapter)(nil)
	_ session.TaskSessionFinder = (*sessionDBAdapter)(nil)
)

func newSessionDBAdapter(db *store.DB) *sessionDBAdapter {
	return &sessionDBAdapter{db: db}
}

func (a *sessionDBAdapter) SaveSession(s session.Session) error {
	return a.db.SaveSession(store.Session(s))
}

func (a *sessionDBAdapter) GetSession(id string) (session.Session, bool, error) {
	s, found, err := a.db.GetSession(id)
	return session.Session(s), found, err
}

func (a *sessionDBAdapter) Touch(id string, at time.Time) error {
	return a.db.Touch(id, at)
}

func (a *sessionDBAdapter) FindSessionByTask(task string) (session.Session, bool, error) {
	s, found, err := a.db.FindSessionByTask(task)
	return session.Session(s), found, err
}
