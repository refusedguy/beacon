package taskcli

import (
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// recheckGuard хранит мьютекс сериализации post-commit recheck.
// Встроен в Service, чтобы состояние recheck лежало рядом с логикой.
type recheckGuard struct {
	recheckMu sync.Mutex
}

// ---- CBX-01: post-commit recheck of dependent blocked flags ----
//
// A close writes the card under a lock. The set of dependent cards is captured
// at that point, then recomputed here from a fresh card read after the write
// is published. RecheckBlocked never runs inside the close transaction and
// never turns a failed recheck into a close error.

const (
	recheckReportName = "recheck.jsonl"
	recheckCountName  = "recheck-count.txt"
)

// RecheckReport is the observable outcome of one recheck pass.
// Rechecked lists cards whose flag was checked, Changed lists cards whose
// status was written, and Failed lists cards whose write did not land.
type RecheckReport struct {
	Rechecked []string
	Changed   []string
	Failed    []string
}

// RecheckReportEntry is one line in the recheck report.
type RecheckReportEntry struct {
	Time      string   `json:"time"`
	Rechecked []string `json:"rechecked,omitempty"`
	Changed   []string `json:"changed,omitempty"`
	Failed    []string `json:"failed,omitempty"`
}

// RecheckBlocked recomputes the blocked flag of every card that depends on one
// of changedIDs. The cards are loaded after the close commit, so two parallel
// closers converge once both writes are visible. A write failure is recorded
// in the report and returned in Failed, not raised as an error.
func (s *Service) RecheckBlocked(changedIDs []string, now time.Time) RecheckReport {
	s.recheckMu.Lock()
	defer s.recheckMu.Unlock()

	rep := RecheckReport{}
	cards, err := LoadCards(s.Cfg.TasksDir)
	if err != nil {
		rep.Failed = append(rep.Failed, "read: "+err.Error())
		s.appendRecheckReport(rep, now)
		return rep
	}

	scope := map[string]bool{}
	for _, id := range taskstore.DependentsOf(taskstore.BlockedGraph(cards), changedIDs) {
		scope[id] = true
	}
	byID := make(map[string]Card, len(cards))
	for _, c := range cards {
		byID[c.ID] = c
	}

	write := s.Cfg.RecheckWriteCard
	if write == nil {
		write = func(path string, data []byte) error {
			return taskstore.AtomicWrite(path, data, 0644)
		}
	}
	for _, d := range taskstore.RecheckDecisions(cards, scope) {
		rep.Rechecked = append(rep.Rechecked, d.ID)
		if d.Current == d.Want {
			continue
		}
		c, ok := byID[d.ID]
		if !ok {
			rep.Failed = append(rep.Failed, d.ID)
			continue
		}
		updated := taskstore.SetCardStatus(c.Raw, d.Want)
		if updated == c.Raw {
			continue
		}
		if err := write(c.Path, []byte(updated)); err != nil {
			rep.Failed = append(rep.Failed, d.ID)
			continue
		}
		rep.Changed = append(rep.Changed, d.ID)
	}
	if err := s.appendRecheckReport(rep, now); err != nil {
		rep.Failed = append(rep.Failed, "report: "+err.Error())
	}
	return rep
}
