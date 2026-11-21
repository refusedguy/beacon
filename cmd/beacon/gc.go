package main

import (
	"flag"
	"fmt"
	"net/url"
	"strconv"

	"github.com/refusedguy/beacon/internal/infra/opencode"
)

// Q-A-02: the lease/ticket collector in watch.go stays intact. This file adds
// the other half of `beacon gc`: dead and orphaned opencode serve processes.
// Only the user's live registration and Beacon's own recorded server are
// protected; everything else with `opencode serve --hostname` is an orphan.

const (
	gcKindServe    gcKind = "serve"
	gcReasonOrphan        = "orphan"
)

// The seams keep process discovery and termination out of the command's code
// path in tests. Production falls through to the infra package.
var (
	gcServeList  = opencode.ListServeProcesses
	gcServicePID = opencode.ReadServicePID
	gcServiceCfg = opencode.ReadConfig
	gcServeState = opencode.ReadServeState
	gcServeClear = opencode.ClearServeState
	gcServeKill  = opencode.TerminateServe
)

func init() {
	RegisterGC(runGCWithServe)
}

// gcPortFromURL returns the port of a service URL. An empty result means the
// registration has no usable port and cannot protect a process by port.
func gcPortFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Port()
}

// gcServePlan classifies serve processes. The main session is protected by
// the pid in service.json, and by port when the registration has no pid.
// Beacon's own recorded server is protected the same way. A recorded pid that
// is no longer alive becomes a stale record to remove.
func gcServePlan(serves []opencode.ServeProcess, servicePID int, servicePort string, recorded opencode.ServeState, hasRecord bool) []gcItem {
	protectedPIDs := make(map[int]bool)
	protectedPorts := make(map[string]bool)
	if servicePID > 0 {
		protectedPIDs[servicePID] = true
	}
	if hasRecord && recorded.PID > 0 {
		protectedPIDs[recorded.PID] = true
	}
	if servicePort != "" {
		protectedPorts[servicePort] = true
	}
	if port := gcPortFromURL(recorded.URL); port != "" {
		protectedPorts[port] = true
	}

	items := make([]gcItem, 0)
	if hasRecord && recorded.PID > 0 && !gcProcessAlive(recorded.PID) {
		items = append(items, gcItem{
			Kind:   gcKindServe,
			ID:     "state:" + strconv.Itoa(recorded.PID),
			Reason: gcReasonDead,
		})
	}
	for _, proc := range serves {
		if protectedPIDs[proc.PID] {
			continue
		}
		if proc.Port != "" && protectedPorts[proc.Port] {
			continue
		}
		items = append(items, gcItem{
			Kind:   gcKindServe,
			ID:     strconv.Itoa(proc.PID),
			Reason: gcReasonOrphan,
		})
	}
	return items
}

// runGCWithServe keeps the read-only/apply split of the existing collector.
// A plain run prints the plan; --apply removes stale state and terminates the
// orphaned serve processes it found.
func runGCWithServe(c *CLI, args []string) int {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода: table или json")
	apply := fs.Bool("apply", false, "удалить найденные записи и terminate сироты")
	if !parseFlags("gc", args, c.errOut, fs) {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(c.errOut, "beacon gc: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(c.errOut)
		return 2
	}

	plan := make([]gcItem, 0)
	var legacy []gcItem
	if state, err := gcReadState(); err == nil {
		legacy = gcStale(state, gcNow())
		plan = append(plan, legacy...)
	}

	serves, err := gcServeList()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon gc: чтение процессов: %v\n", err)
		return 2
	}
	servicePort := ""
	if cfg, err := gcServiceCfg(); err == nil {
		servicePort = gcPortFromURL(cfg.URL)
	}
	recorded, hasRecord := gcServeState()
	servePlan := gcServePlan(serves, gcServicePID(), servicePort, recorded, hasRecord)
	plan = append(plan, servePlan...)

	if !*apply {
		if *format == "json" {
			if err := renderGCJSON(c.out, gcResult{Mode: "dry-run", Items: plan}); err != nil {
				fmt.Fprintf(c.errOut, "beacon gc: %v\n", err)
				return 2
			}
			return 0
		}
		renderGCTable(c.out, plan, false)
		return 0
	}

	removed := make([]gcItem, 0)
	if legacy != nil {
		done, err := gcWriteState(legacy)
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon gc: удаление записей: %v\n", err)
			return 2
		}
		removed = append(removed, done...)
	}
	for _, item := range servePlan {
		if item.Kind != gcKindServe || item.Reason != gcReasonOrphan {
			continue
		}
		pid, err := strconv.Atoi(item.ID)
		if err != nil {
			continue
		}
		if err := gcServeKill(pid); err != nil {
			fmt.Fprintf(c.errOut, "beacon gc: terminate pid %d: %v\n", pid, err)
			return 2
		}
		removed = append(removed, item)
	}
	if hasRecord && recorded.PID > 0 && !gcProcessAlive(recorded.PID) {
		if err := gcServeClear(); err != nil {
			fmt.Fprintf(c.errOut, "beacon gc: очистка записи сервера: %v\n", err)
			return 2
		}
		removed = append(removed, gcItem{
			Kind:   gcKindServe,
			ID:     "state:" + strconv.Itoa(recorded.PID),
			Reason: gcReasonDead,
		})
	}
	if removed == nil {
		removed = []gcItem{}
	}
	if *format == "json" {
		if err := renderGCJSON(c.out, gcResult{Mode: "applied", Removed: len(removed), Items: removed}); err != nil {
			fmt.Fprintf(c.errOut, "beacon gc: %v\n", err)
			return 2
		}
		return 0
	}
	renderGCTable(c.out, removed, true)
	return 0
}
