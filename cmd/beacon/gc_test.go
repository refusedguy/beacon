package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/opencode"
)

func mockServeGC(t *testing.T) (*bytes.Buffer, *bytes.Buffer, *[]int, *int) {
	t.Helper()
	oldList, oldPID, oldCfg := gcServeList, gcServicePID, gcServiceCfg
	oldState, oldClear, oldKill := gcServeState, gcServeClear, gcServeKill
	oldAlive, oldRead, oldWrite := gcProcessAlive, gcReadState, gcWriteState
	t.Cleanup(func() {
		gcServeList, gcServicePID, gcServiceCfg = oldList, oldPID, oldCfg
		gcServeState, gcServeClear, gcServeKill = oldState, oldClear, oldKill
		gcProcessAlive, gcReadState, gcWriteState = oldAlive, oldRead, oldWrite
	})

	killed := make([]int, 0)
	cleared := 0
	gcServeKill = func(pid int) error {
		killed = append(killed, pid)
		return nil
	}
	gcServeClear = func() error {
		cleared++
		return nil
	}
	gcProcessAlive = func(pid int) bool { return pid > 0 && pid < 500 }
	gcReadState = func() (gcState, error) { return gcState{}, nil }
	gcWriteState = func([]gcItem) ([]gcItem, error) { return []gcItem{}, nil }
	gcServiceCfg = func() (opencode.Config, error) {
		return opencode.Config{URL: "http://127.0.0.1:4096"}, nil
	}

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return out, errOut, &killed, &cleared
}

func TestQA2ServePlanProtectsMainAndOwnServer(t *testing.T) {
	out, errOut, killed, cleared := mockServeGC(t)
	_ = out
	_ = errOut
	_ = killed
	_ = cleared

	serves := []opencode.ServeProcess{
		{PID: 100, Port: "4096"}, // main session from service.json
		{PID: 200, Port: "5000"}, // Beacon's own recorded server
		{PID: 300, Port: "6000"}, // orphan
		{PID: 400, Port: ""},     // orphan without a port
	}
	recorded := opencode.ServeState{URL: "http://127.0.0.1:5000", PID: 200}
	plan := gcServePlan(serves, 100, "4096", recorded, true)
	if len(plan) != 2 {
		t.Fatalf("plan = %+v, want 2 orphans", plan)
	}
	for _, item := range plan {
		if item.ID == "100" || item.ID == "200" {
			t.Fatalf("защищённый процесс попал в план: %+v", item)
		}
		if item.Kind != gcKindServe || item.Reason != gcReasonOrphan {
			t.Fatalf("неверный item: %+v", item)
		}
	}
}

func TestQA2ServePlanProtectsMainByPort(t *testing.T) {
	mockServeGC(t)
	serves := []opencode.ServeProcess{
		{PID: 700, Port: "4096"}, // no pid in service.json, port still protects
		{PID: 800, Port: "6000"},
	}
	plan := gcServePlan(serves, 0, "4096", opencode.ServeState{}, false)
	if len(plan) != 1 || plan[0].ID != "800" {
		t.Fatalf("plan = %+v, want only pid 800", plan)
	}
}

func TestQA2ServePlanMarksDeadRecord(t *testing.T) {
	mockServeGC(t)
	gcProcessAlive = func(pid int) bool { return false }
	recorded := opencode.ServeState{URL: "http://127.0.0.1:5000", PID: 999}
	plan := gcServePlan(nil, 0, "", recorded, true)
	if len(plan) != 1 || plan[0].ID != "state:999" || plan[0].Reason != gcReasonDead {
		t.Fatalf("plan = %+v, want stale state:999", plan)
	}
}

func TestQA2GCDryRunDoesNotKill(t *testing.T) {
	out, errOut, killed, cleared := mockServeGC(t)
	gcServeList = func() ([]opencode.ServeProcess, error) {
		return []opencode.ServeProcess{{PID: 100, Port: "4096"}, {PID: 300, Port: "6000"}}, nil
	}
	gcServicePID = func() int { return 100 }
	gcServeState = func() (opencode.ServeState, bool) {
		return opencode.ServeState{URL: "http://127.0.0.1:5000", PID: 200}, true
	}

	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, nil); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	if len(*killed) != 0 || *cleared != 0 {
		t.Fatalf("dry-run killed %v cleared %d", *killed, *cleared)
	}
	if !strings.Contains(out.String(), "300") || strings.Contains(out.String(), "100") {
		t.Fatalf("out = %q", out.String())
	}
	if !strings.Contains(out.String(), "НАЙДЕНО: 1") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestQA2GCApplyKillsOnlyOrphans(t *testing.T) {
	out, errOut, killed, cleared := mockServeGC(t)
	gcServeList = func() ([]opencode.ServeProcess, error) {
		return []opencode.ServeProcess{
			{PID: 100, Port: "4096"},
			{PID: 200, Port: "5000"},
			{PID: 300, Port: "6000"},
			{PID: 400, Port: ""},
		}, nil
	}
	gcServicePID = func() int { return 100 }
	gcServeState = func() (opencode.ServeState, bool) {
		return opencode.ServeState{URL: "http://127.0.0.1:5000", PID: 200}, true
	}

	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--apply"}); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	if len(*killed) != 2 || (*killed)[0] != 300 || (*killed)[1] != 400 {
		t.Fatalf("killed = %v, want [300 400]", *killed)
	}
	if *cleared != 0 {
		t.Fatalf("живая запись очищена: %d", *cleared)
	}
	if !strings.Contains(out.String(), "УДАЛЕНО: 2") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestQA2GCApplyClearsDeadRecord(t *testing.T) {
	out, errOut, killed, cleared := mockServeGC(t)
	gcServeList = func() ([]opencode.ServeProcess, error) { return nil, nil }
	gcServeState = func() (opencode.ServeState, bool) {
		return opencode.ServeState{URL: "http://127.0.0.1:5000", PID: 999}, true
	}
	gcProcessAlive = func(pid int) bool { return false }

	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--apply", "--format", "json"}); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	if *cleared != 1 {
		t.Fatalf("cleared = %d, want 1", *cleared)
	}
	if len(*killed) != 0 {
		t.Fatalf("killed = %v", *killed)
	}
	if !strings.Contains(out.String(), `"mode":"applied"`) || !strings.Contains(out.String(), "state:999") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestQA2GCListFailure(t *testing.T) {
	out, errOut, _, _ := mockServeGC(t)
	gcServeList = func() ([]opencode.ServeProcess, error) {
		return nil, errors.New("/proc недоступен")
	}
	c := &CLI{out: out, errOut: errOut}
	if code := runGCWithServe(c, []string{"--apply"}); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}
