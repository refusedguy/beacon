package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// cardsPath is the repository pathspec for task cards. Creation and completion
// share it so no caller hardcodes where cards live.
const cardsPath = "docs/tasks"

// cardPath is the repository path of one card. It is the single place that
// turns a task ID into a file path.
func cardPath(id core.TaskID) string { return cardsPath + "/" + string(id) + ".md" }

// IDFactory assigns IDs to new cards. Epic IDs stay stable for the same title
// and plan, and batch IDs depend only on the batch contents. Neither derived
// ID reads the clock, so the same content gets the same ID on any day. New
// assigns an unconstrained random ID and is the only method that uses a date.
type IDFactory interface {
	New() (core.TaskID, error)
	ForEpic(title string, plan []core.ChildSpec) core.TaskID
	ForBatch(seed string, index int) core.TaskID
}

// defaultIDFactory carries an optional explicit date for New. An empty Date
// means today. Derived IDs ignore it.
type defaultIDFactory struct{ Date string }

// New returns a random task ID dated with f.Date, or today's date when it is empty.
func (f defaultIDFactory) New() (core.TaskID, error) { return newTaskID(f.Date) }

// ForEpic derives an epic ID from the title and the planned children.
func (defaultIDFactory) ForEpic(title string, plan []core.ChildSpec) core.TaskID {
	return epicTaskID(title, plan)
}

// ForBatch derives a card ID from the batch seed and the card's index.
func (defaultIDFactory) ForBatch(seed string, index int) core.TaskID {
	return batchTaskID(seed, index)
}

// epicTaskID derives the ID from the title and sorted child titles alone. No
// clock is read, so creating the same epic on a later day keeps its ID.
func epicTaskID(title string, plan []core.ChildSpec) core.TaskID {
	sum := sha256.Sum256([]byte(title + "\x00" + strings.Join(childTitles(plan), "\x00")))
	return core.TaskID("T-" + hex.EncodeToString(sum[:4]))
}

// batchTaskID derives the ID from the batch seed and position alone. No clock
// is read, so the same batch on a later day keeps its IDs.
func batchTaskID(seed string, index int) core.TaskID {
	sum := sha256.Sum256([]byte(seed + "\x00" + strconv.Itoa(index)))
	return core.TaskID("T-" + hex.EncodeToString(sum[:4]))
}

// childTitles returns the trimmed child titles in sorted order.
func childTitles(specs []core.ChildSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, strings.TrimSpace(spec.Title))
	}
	sort.Strings(out)
	return out
}

// taskTitles returns the trimmed task titles in sorted order.
func taskTitles(tasks []core.Task) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, strings.TrimSpace(task.Title))
	}
	sort.Strings(out)
	return out
}

// newTaskID assigns an unconstrained random ID. The date is explicit so a
// caller can pin it; an empty date means today. This is the only ID generator
// that reads the clock, and it never participates in content-derived IDs.
func newTaskID(date string) (core.TaskID, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	if date == "" {
		date = time.Now().UTC().Format("20060102")
	}
	return core.TaskID("T-" + date + "-" + hex.EncodeToString(b[:])), nil
}
