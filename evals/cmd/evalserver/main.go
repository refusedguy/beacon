//go:build eval

// Command evalserver поднимает MCP-сервер beacon с реальными зависимостями
// и отдаёт его через stdio. Это eval-специфичная точка входа: штатная команда
// `beacon mcp` всегда вызывает mcp.Run с NewServer() без Store, поэтому
// stateful-инструменты там ответят PERSIST_FAILED. Здесь используется тот же
// mcp.NewServerWithDeps и те же конструкторы, что и в тестах сервера.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

func main() {
	// Отдельные режимы проверок. В обычном прогоне evalserver запускается без
	// аргументов как MCP-сервер.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--drift-check", "drift-check":
			os.Exit(runDriftCheck())
		case "--severity-map-check", "severity-map-check":
			os.Exit(runSeverityMapCheck())
		case "--report-check", "report-check":
			os.Exit(runReportCheck())
		}
	}
	if err := serve(); err != nil {
		fmt.Fprintf(os.Stderr, "evalserver: %v\n", err)
		os.Exit(1)
	}
}

// Файловый доступ проверки дрейфа собран в этой точке входа: capabilities-гейт
// разрешает файловые вызовы в evals/cmd/evalserver/main.go. Логика сканера в
// drift.go работает только с путями и не обращается к диску напрямую.
func drvReadFile(path string) ([]byte, error)         { return os.ReadFile(path) }
func drvStat(path string) (os.FileInfo, error)        { return os.Stat(path) }
func drvMkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func drvWriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}
func drvRemoveAll(path string) error { return os.RemoveAll(path) }

// HD-047 (ADR 0301). Шкала security-находки из gate result (ADR 0237) не
// является шкалой уровня gate. Мапирование идёт в одну сторону:
//
//	critical, high → error   блокирующая находка
//	medium,  low   → warning неблокирующая по умолчанию
//
// Уровень gate всегда error или warning. Своя шкала, где critical/high/medium/low
// подаются как уровень, в gate result не проходит. Блокируемость warning
// определяет политика: по умолчанию она выключена, поэтому medium/low не
// блокируют. Уровень от политики не зависит, меняется только blocking.
type securitySeverity string

const (
	severityCritical securitySeverity = "critical"
	severityHigh     securitySeverity = "high"
	severityMedium   securitySeverity = "medium"
	severityLow      securitySeverity = "low"
)

var allSeverities = []securitySeverity{
	severityCritical, severityHigh, severityMedium, severityLow,
}

type gateLevel string

const (
	levelError   gateLevel = "error"
	levelWarning gateLevel = "warning"
)

type severityPolicy struct {
	warnBlocking bool
}

type mappedSeverity struct {
	severity securitySeverity
	level    gateLevel
	blocking bool
}

func mapSecuritySeverity(severity securitySeverity, policy severityPolicy) (mappedSeverity, error) {
	switch severity {
	case severityCritical, severityHigh:
		return mappedSeverity{severity: severity, level: levelError, blocking: true}, nil
	case severityMedium, severityLow:
		return mappedSeverity{severity: severity, level: levelWarning, blocking: policy.warnBlocking}, nil
	default:
		return mappedSeverity{}, fmt.Errorf("неизвестный security severity %q: своя шкала без мапирования не допускается", severity)
	}
}

// runSeverityMapCheck делает шкалу HD-047 исполняемой проверкой. Каждый
// негативный контроль проваливает прогон, если мапирование ослабить: вернуть
// сырую шкалу как уровень, сделать medium/low блокирующими по умолчанию,
// принять неизвестный severity или нарушить стабильность результата.
func runSeverityMapCheck() int {
	fmt.Println("HD-047 (ADR 0301): мапирование security severity в уровень gate result")
	fmt.Println()
	fmt.Println("Шкала: critical/high -> error (blocking), medium/low -> warning (неблокирующая по умолчанию)")
	fmt.Println()

	type check struct {
		name string
		run  func() (string, bool)
	}
	checks := []check{
		{"critical->error", func() (string, bool) {
			m, err := mapSecuritySeverity(severityCritical, severityPolicy{})
			return fmt.Sprintf("level=%s blocking=%t", m.level, m.blocking),
				err == nil && m.level == levelError && m.blocking
		}},
		{"high->error", func() (string, bool) {
			m, err := mapSecuritySeverity(severityHigh, severityPolicy{})
			return fmt.Sprintf("level=%s blocking=%t", m.level, m.blocking),
				err == nil && m.level == levelError && m.blocking
		}},
		{"medium->warning", func() (string, bool) {
			m, err := mapSecuritySeverity(severityMedium, severityPolicy{})
			return fmt.Sprintf("level=%s blocking=%t", m.level, m.blocking),
				err == nil && m.level == levelWarning && !m.blocking
		}},
		{"low->warning", func() (string, bool) {
			m, err := mapSecuritySeverity(severityLow, severityPolicy{})
			return fmt.Sprintf("level=%s blocking=%t", m.level, m.blocking),
				err == nil && m.level == levelWarning && !m.blocking
		}},
		{"unknown rejected", func() (string, bool) {
			m, err := mapSecuritySeverity(securitySeverity("info"), severityPolicy{})
			return fmt.Sprintf("error=%t zero=%t", err != nil, m == (mappedSeverity{})),
				err != nil && m == (mappedSeverity{})
		}},
		{"no raw security scale as level", func() (string, bool) {
			leaked := make([]string, 0)
			for _, s := range allSeverities {
				m, err := mapSecuritySeverity(s, severityPolicy{})
				if err != nil || m.level == gateLevel(s) {
					leaked = append(leaked, string(s))
				}
			}
			return fmt.Sprintf("leaked=%v", leaked), len(leaked) == 0
		}},
		{"policy changes only blocking", func() (string, bool) {
			ok, detail := true, ""
			for _, s := range []securitySeverity{severityMedium, severityLow} {
				plain, errPlain := mapSecuritySeverity(s, severityPolicy{})
				escalated, errEscalated := mapSecuritySeverity(s, severityPolicy{warnBlocking: true})
				detail += fmt.Sprintf("%s:%t->%t ", s, plain.blocking, escalated.blocking)
				if errPlain != nil || errEscalated != nil ||
					plain.level != levelWarning || escalated.level != plain.level ||
					plain.blocking || !escalated.blocking {
					ok = false
				}
			}
			return detail, ok
		}},
		{"deterministic for same inputs", func() (string, bool) {
			for _, s := range allSeverities {
				for _, p := range []severityPolicy{{}, {warnBlocking: true}} {
					first, errFirst := mapSecuritySeverity(s, p)
					second, errSecond := mapSecuritySeverity(s, p)
					if errFirst != errSecond || first != second {
						return fmt.Sprintf("severity=%s warnBlocking=%t", s, p.warnBlocking), false
					}
				}
			}
			return "all severities and policies stable", true
		}},
	}

	ok := true
	for _, c := range checks {
		actual, pass := c.run()
		status := "PASS"
		if !pass {
			status, ok = "FAIL", false
		}
		fmt.Printf("  %s %s: %s\n", status, c.name, actual)
	}
	fmt.Println()
	if !ok {
		fmt.Println("Результат: FAIL. Мапирование severity не соблюдено")
		return 1
	}
	fmt.Println("Результат: PASS")
	return 0
}

// HD-084 (ADR 0310). Отчёт ночной смены формируется по слотам и решениям.
// Множество категорий фиксировано в docs/evals/hd40-weekly-report.md: пропуск
// любой категории проваливает проверку.
type shiftCategory string

const (
	shiftMerged        shiftCategory = "merged"
	shiftGreenUnmerged shiftCategory = "green_unmerged"
	shiftRedAgented    shiftCategory = "red_agented"
	shiftRespawned     shiftCategory = "respawned"
	shiftQueueLogic    shiftCategory = "queue_logic"
	shiftOwnerDecision shiftCategory = "owner_decisions"
)

var allShiftCategories = []shiftCategory{
	shiftMerged, shiftGreenUnmerged, shiftRedAgented,
	shiftRespawned, shiftQueueLogic, shiftOwnerDecision,
}

// shiftReport — шаблон отчёта смены. Каждая категория присутствует слотом,
// даже если слот пока пуст. Отчёт по слотам и решениям заполняет слоты строками.
type shiftReport struct {
	slots map[shiftCategory][]string
}

// newShiftReportTemplate возвращает шаблон со всеми обязательными категориями.
func newShiftReportTemplate() *shiftReport {
	r := &shiftReport{slots: make(map[shiftCategory][]string, len(allShiftCategories))}
	for _, c := range allShiftCategories {
		r.slots[c] = nil
	}
	return r
}

func (r *shiftReport) add(c shiftCategory, line string) {
	r.slots[c] = append(r.slots[c], line)
}

// missingCategories возвращает категории, отсутствующие в отчёте.
func (r *shiftReport) missingCategories() []shiftCategory {
	missing := make([]shiftCategory, 0)
	for _, c := range allShiftCategories {
		if _, ok := r.slots[c]; !ok {
			missing = append(missing, c)
		}
	}
	return missing
}

// filledShiftReport — эталон: все слоты заполнены, пропущенных категорий нет.
func filledShiftReport() *shiftReport {
	r := newShiftReportTemplate()
	for _, c := range allShiftCategories {
		r.add(c, "sample")
	}
	return r
}

// runReportCheck делает шаблон отчёта смены исполняемой проверкой. Позитивный
// контроль требует все категории в шаблоне и в заполненном отчёте. Негативный
// контроль проваливает отчёт с пропущенной категорией.
func runReportCheck() int {
	fmt.Println("HD-084 (ADR 0310): отчёт ночной смены по слотам и решениям")
	fmt.Println()
	fmt.Println("Категории: merged, green_unmerged, red_agented, respawned, queue_logic, owner_decisions")
	fmt.Println()

	type check struct {
		name string
		run  func() (string, bool)
	}
	checks := []check{
		{"template lists all categories", func() (string, bool) {
			missing := newShiftReportTemplate().missingCategories()
			return fmt.Sprintf("missing=%v", missing), len(missing) == 0
		}},
		{"required categories present", func() (string, bool) {
			want := []string{"merged", "green_unmerged", "red_agented", "respawned", "queue_logic", "owner_decisions"}
			have := make(map[string]bool, len(allShiftCategories))
			for _, c := range allShiftCategories {
				have[string(c)] = true
			}
			for _, w := range want {
				if !have[w] {
					return fmt.Sprintf("missing=%s", w), false
				}
			}
			return fmt.Sprintf("categories=%d", len(have)), true
		}},
		{"filled report has all categories", func() (string, bool) {
			missing := filledShiftReport().missingCategories()
			return fmt.Sprintf("missing=%v", missing), len(missing) == 0
		}},
		{"omitted category fails", func() (string, bool) {
			for _, omitted := range allShiftCategories {
				r := filledShiftReport()
				delete(r.slots, omitted)
				missing := r.missingCategories()
				if len(missing) != 1 || missing[0] != omitted {
					return fmt.Sprintf("omitted=%s missing=%v", omitted, missing), false
				}
			}
			return "every omitted category rejected", true
		}},
	}

	ok := true
	for _, c := range checks {
		actual, pass := c.run()
		status := "PASS"
		if !pass {
			status, ok = "FAIL", false
		}
		fmt.Printf("  %s %s: %s\n", status, c.name, actual)
	}
	fmt.Println()
	if !ok {
		fmt.Println("Результат: FAIL. Отчёт смены без категорий не проходит")
		return 1
	}
	fmt.Println("Результат: PASS")
	return 0
}

func serve() error {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		return fmt.Errorf("XDG_STATE_HOME не задан")
	}
	if err := os.MkdirAll(stateHome, 0o700); err != nil {
		return fmt.Errorf("создать XDG_STATE_HOME: %w", err)
	}

	db, err := store.Open(filepath.Join(stateHome, "beacon.db"))
	if err != nil {
		return fmt.Errorf("открыть store: %w", err)
	}
	defer func() { _ = db.Close() }()

	deps := mcp.Dependencies{
		Store:   db,
		Stamper: mcp.NewSQLObligationStamper(db),
		Writer:  mcp.NewSQLWaitResultWriter(db),
		Events:  mcp.NewSQLCancelEventLog(db),
		Snapshot: mcp.SnapshotFunc(func() (mcp.StateSnapshot, error) {
			watches, err := db.ActiveWatches()
			if err != nil {
				return mcp.StateSnapshot{}, err
			}
			return mcp.StateSnapshot{ActiveWatches: watches, Version: mcp.ServerVersion}, nil
		}),
	}

	server := mcp.NewServerWithDeps(deps)

	// NewServerWithDeps registers list_running without an explicit output
	// schema, so go-sdk derives one by reflection and treats a nil
	// domain.FlakyConfig.Used map as an invalid "null". Supply the corrected
	// schema from mcp.AllOutputSchemas; the handler stays the production one.
	schemas, err := mcp.AllOutputSchemas()
	if err != nil {
		return fmt.Errorf("output schemas: %w", err)
	}
	no := false
	server.RemoveTools("beacon_list_running")
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:         "beacon_list_running",
		Description:  "List running Watches from a read-only state snapshot.",
		OutputSchema: schemas["beacon_list_running"],
		Annotations: &mcpsdk.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: &no,
		},
	}, mcp.NewListRunningHandler(deps.Snapshot).Handle)

	return server.Run(context.Background(), &mcpsdk.StdioTransport{})
}
