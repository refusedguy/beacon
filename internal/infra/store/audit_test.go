package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openAuditDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func auditAt(second int) time.Time {
	return time.Date(2026, 11, 20, 10, 0, second, 0, time.UTC)
}

// The row is written through d.Write, so it must be visible to a reader on
// the separate read pool after the call returns.
func TestAuditRecordInTransaction(t *testing.T) {
	d := openAuditDB(t)
	want := AuditEntry{
		Actor:     "ses_executor",
		Ingress:   AuditIngressMCP,
		CommandID: "cmd-1",
		Action:    "approve",
		At:        auditAt(1),
	}
	if err := d.RecordAudit(want); err != nil {
		t.Fatalf("RecordAudit: %v", err)
	}

	got, err := d.ListAudit(AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListAudit returned %d entries, want 1", len(got))
	}
	if got[0].Actor != want.Actor || got[0].Ingress != want.Ingress ||
		got[0].CommandID != want.CommandID || got[0].Action != want.Action {
		t.Errorf("audit entry = %+v, want %+v", got[0], want)
	}
	if !got[0].At.Equal(want.At) {
		t.Errorf("At = %v, want %v", got[0].At, want.At)
	}
	requireTable(t, d.readDB, "audit")
}

// RecordAuditTx is the primitive a privileged command uses to audit itself in
// the same transaction. A rolled-back transaction must leave no audit row.
func TestAuditRollbackLeavesNoRecord(t *testing.T) {
	d := openAuditDB(t)
	boom := errors.New("command failed")
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := RecordAuditTx(tx, AuditEntry{
			Actor:     "ses_executor",
			Ingress:   AuditIngressTUI,
			CommandID: "cmd-rollback",
			Action:    "merge",
			At:        auditAt(2),
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Write error = %v, want command failure", err)
	}

	n, err := d.AuditCount()
	if err != nil {
		t.Fatalf("AuditCount: %v", err)
	}
	if n != 0 {
		t.Errorf("AuditCount after rollback = %d, want 0", n)
	}
	got, err := d.ListAudit(AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListAudit after rollback = %+v, want empty", got)
	}
}

func TestAuditFilterByIngressAndActor(t *testing.T) {
	d := openAuditDB(t)
	entries := []AuditEntry{
		{Actor: "alice", Ingress: AuditIngressMCP, CommandID: "c1", Action: "merge", At: auditAt(1)},
		{Actor: "alice", Ingress: AuditIngressTUI, CommandID: "c2", Action: "wake", At: auditAt(2)},
		{Actor: "bob", Ingress: AuditIngressMCP, CommandID: "c3", Action: "merge", At: auditAt(3)},
		{Actor: "bob", Ingress: AuditIngressWebhook, CommandID: "c4", Action: "wake", At: auditAt(4)},
	}
	for _, e := range entries {
		if err := d.RecordAudit(e); err != nil {
			t.Fatalf("RecordAudit(%s): %v", e.CommandID, err)
		}
	}

	all, err := d.ListAudit(AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit(all): %v", err)
	}
	if len(all) != len(entries) {
		t.Fatalf("ListAudit(all) = %d entries, want %d", len(all), len(entries))
	}
	for i, e := range all {
		if e.CommandID != entries[i].CommandID {
			t.Errorf("entry %d = %s, want %s (oldest first)", i, e.CommandID, entries[i].CommandID)
		}
	}

	byActor, err := d.ListAudit(AuditFilter{Actor: "alice"})
	if err != nil {
		t.Fatalf("ListAudit(actor): %v", err)
	}
	if len(byActor) != 2 || byActor[0].CommandID != "c1" || byActor[1].CommandID != "c2" {
		t.Errorf("ListAudit(actor=alice) = %+v, want c1,c2", byActor)
	}

	byIngress, err := d.ListAudit(AuditFilter{Ingress: AuditIngressMCP})
	if err != nil {
		t.Fatalf("ListAudit(ingress): %v", err)
	}
	if len(byIngress) != 2 || byIngress[0].CommandID != "c1" || byIngress[1].CommandID != "c3" {
		t.Errorf("ListAudit(ingress=mcp) = %+v, want c1,c3", byIngress)
	}

	both, err := d.ListAudit(AuditFilter{Actor: "bob", Ingress: AuditIngressWebhook})
	if err != nil {
		t.Fatalf("ListAudit(both): %v", err)
	}
	if len(both) != 1 || both[0].CommandID != "c4" {
		t.Errorf("ListAudit(bob+webhook) = %+v, want c4", both)
	}

	none, err := d.ListAudit(AuditFilter{Actor: "alice", Ingress: AuditIngressWebhook})
	if err != nil {
		t.Fatalf("ListAudit(no match): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ListAudit(no match) = %+v, want empty", none)
	}
}

func TestAuditEmptyDatabase(t *testing.T) {
	d := openAuditDB(t)
	got, err := d.ListAudit(AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit on empty database: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListAudit on empty database = %+v, want empty", got)
	}
	n, err := d.AuditCount()
	if err != nil || n != 0 {
		t.Errorf("AuditCount on empty database = %d, %v; want 0, nil", n, err)
	}
	ok, err := tableExists(context.Background(), d.readDB, "audit")
	if err != nil || ok {
		t.Errorf("audit table exists after read = %v, %v; want false, nil", ok, err)
	}
}

// A merge and a wake are both privileged commands and both carry the actor
// that issued them. They differ by action and may arrive on different
// ingresses.
func TestAuditMergeAndWakeHaveActor(t *testing.T) {
	d := openAuditDB(t)
	if err := d.RecordAudit(AuditEntry{
		Actor: "ses_orchestrator", Ingress: AuditIngressMCP,
		CommandID: "merge-1", Action: "merge", At: auditAt(1),
	}); err != nil {
		t.Fatalf("RecordAudit(merge): %v", err)
	}
	if err := d.RecordAudit(AuditEntry{
		Actor: "webhook-delivery", Ingress: AuditIngressWebhook,
		CommandID: "wake-1", Action: "wake", At: auditAt(2),
	}); err != nil {
		t.Fatalf("RecordAudit(wake): %v", err)
	}

	merge, err := d.ListAudit(AuditFilter{Actor: "ses_orchestrator"})
	if err != nil {
		t.Fatalf("ListAudit(merge): %v", err)
	}
	if len(merge) != 1 || merge[0].Action != "merge" || merge[0].Ingress != AuditIngressMCP {
		t.Fatalf("merge audit = %+v, want one mcp merge", merge)
	}
	wake, err := d.ListAudit(AuditFilter{Actor: "webhook-delivery"})
	if err != nil {
		t.Fatalf("ListAudit(wake): %v", err)
	}
	if len(wake) != 1 || wake[0].Action != "wake" || wake[0].Ingress != AuditIngressWebhook {
		t.Fatalf("wake audit = %+v, want one webhook wake", wake)
	}
}

func TestAuditRejectsIncompleteEntry(t *testing.T) {
	d := openAuditDB(t)
	valid := AuditEntry{
		Actor: "alice", Ingress: AuditIngressTUI,
		CommandID: "c1", Action: "cancel", At: auditAt(1),
	}
	cases := []struct {
		name  string
		entry AuditEntry
	}{
		{"empty actor", func() AuditEntry { e := valid; e.Actor = "  "; return e }()},
		{"unknown ingress", func() AuditEntry { e := valid; e.Ingress = "socket"; return e }()},
		{"empty command id", func() AuditEntry { e := valid; e.CommandID = ""; return e }()},
		{"empty action", func() AuditEntry { e := valid; e.Action = ""; return e }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := d.RecordAudit(tc.entry); err == nil {
				t.Fatal("RecordAudit accepted an incomplete entry")
			}
		})
	}
	n, err := d.AuditCount()
	if err != nil || n != 0 {
		t.Errorf("AuditCount after rejected writes = %d, %v; want 0, nil", n, err)
	}
}
