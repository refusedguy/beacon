// Package taskcli содержит бизнес-логику подкоманды beacon task.
// Каталог карточек, каталог состояния и каталог ADR передаются конфигурацией,
// поэтому тесты работают во временных каталогах.

package taskcli

import (
	"context"
	"time"
)

// DefaultLockRetryInterval совпадает с шагом повтора taskstore.AcquireLock.
const DefaultLockRetryInterval = 100 * time.Millisecond

// Config задаёт каталоги и корень проекта.
type Config struct {
	TasksDir string
	StateDir string
	ADRDir   string
	Root     string

	// LockRetryInterval задаёт паузу между попытками взять общий файловый
	// замок. Ноль и отрицательное значение включают поведение по умолчанию:
	// вызов идёт через taskstore.AcquireLock с его шагом 100 мс. Тесты ставят
	// короткий интервал, не меняя production-путь.
	LockRetryInterval time.Duration

	// HooksDir задаёт каталог сценариев жизненного цикла карточки. Пустое
	// значение отключает хуки и не создаёт файл журнала.
	HooksDir string

	// RecheckWriteCard заменяет запись карточки при post-commit recheck.
	// Пустое значение означает обычную запись через taskstore.AtomicWrite.
	// Тесты подставляют сюда writers с искусственной ошибкой.
	RecheckWriteCard func(path string, data []byte) error

	// DeferWakeNow фиксирует часы пробуждения по дате отложки. Пустое значение
	// означает time.Now().UTC(). Тесты ставят фиксированное время.
	DeferWakeNow func() time.Time
	// DeferWakeReadCard заменяет повторное чтение карточки перед записью.
	// Пустое значение означает os.ReadFile. Тесты моделируют конкурента,
	// который взял карточку между снимком и записью.
	DeferWakeReadCard func(path string) ([]byte, error)
	// DeferWakeWriteCard заменяет запись карточки при пробуждении. Пустое
	// значение означает AtomicWrite. Тесты подставляют writers с ошибкой.
	DeferWakeWriteCard func(path string, data []byte) error
}

// GateWaiterRegistrar — app-порт конкурентной регистрации ожидающих на гейте
// (CBX-11). Инфраструктурная реализация использует CAS с джиттерованным
// backoff. Значение по умолчанию ничего не делает, поэтому существующие
// вызывающие RunHooks не меняют поведение.
type GateWaiterRegistrar interface {
	Register(ctx context.Context, gateID, waiterID string) error
}

type noopGateWaiterRegistrar struct{}

func (noopGateWaiterRegistrar) Register(context.Context, string, string) error { return nil }

// Service выполняет list, new и take.
type Service struct {
	Cfg Config

	// store is the task-storage port. NewService installs the file adapter;
	// SetTaskStore replaces it without changing any command contract.
	store TaskStore

	// gateWaiters регистрирует ожидающего на гейте при событии unblocked.
	// Пустое значение означает no-op.
	gateWaiters GateWaiterRegistrar

	// recheckGuard сериализует post-commit recheck. Мьютекс живёт в recheck.go.
	recheckGuard

	// loadCards читает карточки. Тесты подменяют его, чтобы считать чтения
	// и доказать, что неверный запрос отклонён до первого чтения.
	loadCards func(string) ([]Card, error)
}

// loadReapCards читает карточки через подменяемый в тестах порт.
func (s *Service) loadReapCards(tasksDir string) ([]Card, error) {
	if s.loadCards != nil {
		return s.loadCards(tasksDir)
	}
	return LoadCards(tasksDir)
}

// NewService создаёт сервис с конфигурацией.
func NewService(cfg Config) *Service {
	s := &Service{Cfg: cfg, gateWaiters: noopGateWaiterRegistrar{}}
	fileStore := NewFileTaskStore(cfg.TasksDir, cfg.Root, nil)
	fileStore.Acquire = s.acquireLock
	s.store = fileStore
	return s
}

// SetTaskStore подменяет хранилище задач. Пустой аргумент возвращает файловый
// адаптер, поэтому вызывающему не нужно знать, какой бэкенд стоит по умолчанию.
func (s *Service) SetTaskStore(store TaskStore) {
	if s == nil {
		return
	}
	if store == nil {
		fileStore := NewFileTaskStore(s.Cfg.TasksDir, s.Cfg.Root, nil)
		fileStore.Acquire = s.acquireLock
		s.store = fileStore
		return
	}
	s.store = store
}

// taskStore returns the installed port, creating the file adapter for services
// assembled without NewService.
func (s *Service) taskStore() TaskStore {
	if s != nil && s.store != nil {
		return s.store
	}
	fileStore := NewFileTaskStore(s.Cfg.TasksDir, s.Cfg.Root, nil)
	fileStore.Acquire = s.acquireLock
	return fileStore
}

// RegisterGateWaiter регистрирует ожидающего на гейте через установленный порт.
func (s *Service) RegisterGateWaiter(ctx context.Context, gateID, waiterID string) error {
	if s == nil {
		return nil
	}
	reg := s.gateWaiters
	if reg == nil {
		reg = noopGateWaiterRegistrar{}
	}
	return reg.Register(ctx, gateID, waiterID)
}
