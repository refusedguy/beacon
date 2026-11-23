package taskcli

import (
	"os"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// The task commands keep one shape for parsed cards while service.go names no
// infra package (ARDH-15 DIP). Type aliases keep the facade a thin pass-through;
// the adapter seam lives in this file instead of in the command service.
type (
	Card           = taskstore.Card
	Lock           = taskstore.Lock
	Linkage        = taskstore.Linkage
	LinkagePorts   = taskstore.LinkagePorts
	LinkageVerdict = taskstore.LinkageVerdict
)

const LinkageEmpty = taskstore.LinkageEmpty

func Validationf(format string, a ...any) error { return taskstore.Validationf(format, a...) }
func Startupf(format string, a ...any) error    { return taskstore.Startupf(format, a...) }
func StartupfWithCause(cause error, format string, a ...any) error {
	return taskstore.StartupfWithCause(cause, format, a...)
}
func Code(err error) int { return taskstore.Code(err) }

func LoadCards(tasksDir string) ([]Card, error) { return taskstore.LoadCards(tasksDir) }
func PathsConflict(a, b string) bool            { return taskstore.PathsConflict(a, b) }
func IsID(s string) bool                        { return taskstore.IsID(s) }
func CardPath(tasksDir, id string) string       { return taskstore.CardPath(tasksDir, id) }
func LockPath(stateDir, root string) string     { return taskstore.LockPath(stateDir, root) }
func CounterPath(stateDir, root string) string  { return taskstore.CounterPath(stateDir, root) }

func AcquireLock(lockPath, projectRoot string) (*Lock, error) {
	return taskstore.AcquireLock(lockPath, projectRoot)
}
func ReadCounter(counterPath string) (map[string]int, error) {
	return taskstore.ReadCounter(counterPath)
}
func WriteCounter(counterPath string, groups map[string]int) error {
	return taskstore.WriteCounter(counterPath, groups)
}
func NextID(cards []Card, groups map[string]int, group int) (int, string) {
	return taskstore.NextID(cards, groups, group)
}
func CreateExclusive(filePath string, data []byte) error {
	return taskstore.CreateExclusive(filePath, data)
}
func AtomicWrite(filePath string, data []byte, perm os.FileMode) error {
	return taskstore.AtomicWrite(filePath, data, perm)
}
func ReplaceFields(raw, agent, when string) string {
	return taskstore.ReplaceFields(raw, agent, when)
}
func CheckLinkage(l Linkage, ports LinkagePorts) LinkageVerdict {
	return taskstore.CheckLinkage(l, ports)
}
func ParseLinkage(message string) Linkage { return taskstore.ParseLinkage(message) }
func RequiresLinkage(subject string) bool { return taskstore.RequiresLinkage(subject) }
