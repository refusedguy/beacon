// Package scheduler plans when a task should run. Planning is pure: it reads
// no clock, no file and no socket. The caller supplies "now", the previous run
// and the server's zone, so equal inputs always give an equal plan (ADR 0189,
// ADR 0192, BS.24).
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Kind selects how a schedule fires.
type Kind uint8

const (
	// KindInterval fires repeatedly, one interval after the previous run.
	KindInterval Kind = iota + 1
	// KindCron fires once at the next matching local wall-clock time.
	KindCron
	// KindMark fires when a webhook delivers its mark.
	KindMark
)

// String names a kind for logs and validation errors.
func (k Kind) String() string {
	switch k {
	case KindInterval:
		return "interval"
	case KindCron:
		return "cron"
	case KindMark:
		return "mark"
	default:
		return "unknown"
	}
}

// Schedule is a validated, immutable trigger definition. Build one with
// Interval, FixedTime or Webhook; a zero Schedule fires nothing.
type Schedule struct {
	kind     Kind
	interval time.Duration
	cron     cronSpec
	mark     string
	loc      *time.Location
}

// Kind reports the trigger mode of s.
func (s Schedule) Kind() Kind { return s.kind }

// Interval builds a repeating schedule. expr is a Go duration such as "15m"
// or "1h30m" and must be positive. A nil loc means time.Local.
func Interval(expr string, loc *time.Location) (Schedule, error) {
	d, err := time.ParseDuration(strings.TrimSpace(expr))
	if err != nil {
		return Schedule{}, fmt.Errorf("scheduler: interval %q: %w", expr, err)
	}
	if d <= 0 {
		return Schedule{}, fmt.Errorf("scheduler: interval %q: must be positive", expr)
	}
	return Schedule{kind: KindInterval, interval: d, loc: resolved(loc)}, nil
}

// FixedTime builds a one-shot schedule from a five-field cron expression:
// minute, hour, day of month, month, day of week. Fields are evaluated in the
// loc wall clock. A nil loc means time.Local, the server's zone.
func FixedTime(expr string, loc *time.Location) (Schedule, error) {
	spec, err := parseCron(expr)
	if err != nil {
		return Schedule{}, err
	}
	return Schedule{kind: KindCron, cron: spec, loc: resolved(loc)}, nil
}

// Webhook builds a mark-triggered schedule. An empty mark is refused.
func Webhook(mark string) (Schedule, error) {
	mark = strings.TrimSpace(mark)
	if mark == "" {
		return Schedule{}, fmt.Errorf("scheduler: webhook mark must not be empty")
	}
	return Schedule{kind: KindMark, mark: mark}, nil
}

// Accepts reports whether a delivered webhook mark matches s. Only mark
// schedules accept marks and comparison is exact after trimming space.
func (s Schedule) Accepts(mark string) bool {
	return s.kind == KindMark && s.mark != "" && strings.TrimSpace(mark) == s.mark
}

// Next is the pure result of planning one run. At is the next_run_at instant;
// zero means there is no time-based next run. Done marks a consumed one-shot
// schedule and an event-driven mark schedule. Skipped counts interval
// occurrences collapsed because now was past due, so downtime never replays
// a burst of duplicate runs.
type Next struct {
	At      time.Time
	Done    bool
	Skipped int
}

// NextRun plans the next run from lastRun (zero means the task never ran).
// now and lastRun may carry any zone; comparisons use absolute time and cron
// matching uses the schedule's zone. No clock is read and no I/O is done.
func (s Schedule) NextRun(now, lastRun time.Time) (Next, error) {
	switch s.kind {
	case KindInterval:
		return s.intervalNext(now, lastRun), nil
	case KindCron:
		return s.cronNext(now, lastRun), nil
	case KindMark:
		return Next{Done: true}, nil
	default:
		return Next{}, fmt.Errorf("scheduler: invalid schedule kind %d", s.kind)
	}
}

// intervalNext returns the first boundary at or after now. Boundaries already
// passed are not replayed; they are counted in Skipped.
func (s Schedule) intervalNext(now, lastRun time.Time) Next {
	base := lastRun
	if base.IsZero() {
		base = now
	}
	step := s.interval
	k := int64(1)
	if elapsed := now.Sub(base); elapsed > 0 {
		k = int64(elapsed / step)
		if base.Add(time.Duration(k) * step).Before(now) {
			k++ // round up when now falls between boundaries
		}
		if k < 1 {
			k = 1
		}
	}
	return Next{At: base.Add(time.Duration(k) * step), Skipped: int(k - 1)}
}

// cronNext returns the earliest wall-clock match at or after now in s.loc.
// A recorded run consumes the fixed-time schedule: it fires once.
func (s Schedule) cronNext(now, lastRun time.Time) Next {
	if !lastRun.IsZero() {
		return Next{Done: true}
	}
	candidate := now.In(s.loc).Truncate(time.Minute)
	// Advancing an instant by a minute skips a spring-forward gap and reaches
	// the first occurrence of a repeated fall-back hour. Both DST cases are
	// explicit: no nonexistent local time is ever returned.
	limit := candidate.AddDate(5, 0, 0)
	for candidate.Before(limit) {
		if s.cron.match(candidate) {
			return Next{At: candidate}
		}
		candidate = candidate.Add(time.Minute)
	}
	return Next{Done: true}
}

func resolved(loc *time.Location) *time.Location {
	if loc == nil {
		return time.Local
	}
	return loc
}

// cronSpec is a parsed five-field cron expression.
type cronSpec struct {
	minute cronField
	hour   cronField
	dom    cronField
	month  cronField
	dow    cronField
}

// match applies Vixie cron's day rule: with both day fields restricted,
// either one may match; with one restricted, that one decides.
func (c cronSpec) match(t time.Time) bool {
	if !c.minute.has(t.Minute()) || !c.hour.has(t.Hour()) || !c.month.has(int(t.Month())) {
		return false
	}
	domOK := c.dom.has(t.Day())
	dowOK := c.dow.has(int(t.Weekday()))
	switch {
	case c.dom.any && c.dow.any:
		return true
	case c.dom.any:
		return dowOK
	case c.dow.any:
		return domOK
	default:
		return domOK || dowOK
	}
}

// cronField holds the set of allowed values in a 64-bit mask.
type cronField struct {
	any  bool
	bits uint64
}

func (f cronField) has(v int) bool {
	if f.any {
		return true
	}
	if v < 0 || v >= 64 {
		return false
	}
	return f.bits&(uint64(1)<<uint(v)) != 0
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

func parseCron(expr string) (cronSpec, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return cronSpec{}, fmt.Errorf("scheduler: cron %q: want 5 fields, got %d", expr, len(parts))
	}
	var spec cronSpec
	var err error
	if spec.minute, err = parseCronField(parts[0], 0, 59, nil); err != nil {
		return cronSpec{}, fmt.Errorf("scheduler: cron minute: %w", err)
	}
	if spec.hour, err = parseCronField(parts[1], 0, 23, nil); err != nil {
		return cronSpec{}, fmt.Errorf("scheduler: cron hour: %w", err)
	}
	if spec.dom, err = parseCronField(parts[2], 1, 31, nil); err != nil {
		return cronSpec{}, fmt.Errorf("scheduler: cron day of month: %w", err)
	}
	if spec.month, err = parseCronField(parts[3], 1, 12, monthNames); err != nil {
		return cronSpec{}, fmt.Errorf("scheduler: cron month: %w", err)
	}
	// Sunday may be written as 7; accept it and fold it onto 0.
	if spec.dow, err = parseCronField(parts[4], 0, 7, dowNames); err != nil {
		return cronSpec{}, fmt.Errorf("scheduler: cron day of week: %w", err)
	}
	if !spec.dow.any && spec.dow.has(7) {
		spec.dow.bits &^= uint64(1) << 7
		spec.dow.bits |= 1
	}
	return spec, nil
}

func parseCronField(s string, lo, hi int, names map[string]int) (cronField, error) {
	if s == "*" {
		return cronField{any: true}, nil
	}
	var f cronField
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return cronField{}, fmt.Errorf("empty list item in %q", s)
		}
		step := 1
		body := item
		if i := strings.IndexByte(item, '/'); i >= 0 {
			n, err := strconv.Atoi(strings.TrimSpace(item[i+1:]))
			if err != nil || n <= 0 {
				return cronField{}, fmt.Errorf("bad step in %q", item)
			}
			step = n
			body = strings.TrimSpace(item[:i])
		}
		start, end, err := cronRange(body, lo, hi, names)
		if err != nil {
			return cronField{}, err
		}
		for v := start; v <= end; v += step {
			if v < lo || v > hi {
				return cronField{}, fmt.Errorf("value %d in %q outside %d-%d", v, s, lo, hi)
			}
			f.bits |= uint64(1) << uint(v)
		}
	}
	if f.bits == 0 {
		return cronField{}, fmt.Errorf("no values in %q", s)
	}
	return f, nil
}

func cronRange(body string, lo, hi int, names map[string]int) (int, int, error) {
	value := func(tok string) (int, error) {
		tok = strings.TrimSpace(tok)
		if names != nil {
			if v, ok := names[strings.ToLower(tok)]; ok {
				return v, nil
			}
		}
		v, err := strconv.Atoi(tok)
		if err != nil {
			return 0, fmt.Errorf("bad value %q", tok)
		}
		return v, nil
	}
	if body == "*" {
		return lo, hi, nil
	}
	if i := strings.IndexByte(body, '-'); i >= 0 {
		start, err := value(body[:i])
		if err != nil {
			return 0, 0, err
		}
		end, err := value(body[i+1:])
		if err != nil {
			return 0, 0, err
		}
		if start > end {
			return 0, 0, fmt.Errorf("reversed range %q", body)
		}
		return start, end, nil
	}
	v, err := value(body)
	if err != nil {
		return 0, 0, err
	}
	return v, v, nil
}
