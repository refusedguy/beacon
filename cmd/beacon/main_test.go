package main

import (
	"path/filepath"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/config"
)

func TestProjectRootPrefersEnvironment(t *testing.T) {
	want := t.TempDir()
	t.Setenv("BEACON_PROJECT_ROOT", want)
	got, err := projectRoot()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("projectRoot() = %q, want %q", got, want)
	}
}

func TestProjectRootFallsBackToWorkingDirectory(t *testing.T) {
	t.Setenv("BEACON_PROJECT_ROOT", "")
	want, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	got, err := projectRoot()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("projectRoot() = %q, want %q", got, want)
	}
}

func TestWireServicesConfiguresTaskWriters(t *testing.T) {
	cfg := config.Config{BaseBranch: "main"}
	infra := &wiredInfra{changes: make(chan int, 1)}
	w := wireServices(cfg, t.TempDir(), "", infra)
	if w.taskCreator == nil || w.taskDeleter == nil || w.taskUpdater == nil {
		t.Fatalf("task writers: creator=%v deleter=%v updater=%v",
			w.taskCreator, w.taskDeleter, w.taskUpdater)
	}
	if w.taskCreator.Store != infra.store {
		t.Error("task creator is missing the store")
	}
	if w.taskDeleter.Root.Branch != "main" || w.taskUpdater.Root.Branch != "main" {
		t.Error("task writers did not inherit the base branch")
	}
}
