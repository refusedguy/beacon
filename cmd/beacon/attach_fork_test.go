package main

import (
	"reflect"
	"testing"

	"github.com/refusedguy/beacon/internal/app/session"
)

func TestForkHistoryStopsAtNewestCompleteCompaction(t *testing.T) {
	journal := []attachJournalRecord{
		{Kind: attachJournalMessage, Text: "old-1"},
		{Kind: attachJournalMessage, Text: "old-2"},
		{Kind: attachJournalCompaction, Text: "frozen-prefix", Window: 7, HasWindow: true},
		{Kind: attachJournalMessage, Text: "tail-1"},
		{Kind: attachJournalMessage, Text: "tail-2"},
	}

	compaction, tail := forkHistory(nil, journal)
	if compaction == nil {
		t.Fatal("compaction = nil, want the complete record in the middle")
	}
	if compaction.Text != "frozen-prefix" || compaction.Window != 7 || !compaction.HasWindow {
		t.Fatalf("compaction = %+v, want frozen-prefix with window 7", *compaction)
	}
	want := []attachJournalRecord{journal[3], journal[4]}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("tail = %+v, want %+v", tail, want)
	}
}

func TestForkHistoryIncompleteCompactionReadsWholeJournal(t *testing.T) {
	journal := []attachJournalRecord{
		{Kind: attachJournalMessage, Text: "one"},
		{Kind: attachJournalCompaction, Text: "missing-window"},
		{Kind: attachJournalMessage, Text: "three"},
	}

	compaction, tail := forkHistory(nil, journal)
	if compaction != nil {
		t.Fatalf("compaction = %+v, want nil for an incomplete record", *compaction)
	}
	if !reflect.DeepEqual(tail, journal) {
		t.Fatalf("tail = %+v, want the whole journal %+v", tail, journal)
	}
}

func TestForkHistorySkipsIncompleteAndFindsOlderCompaction(t *testing.T) {
	journal := []attachJournalRecord{
		{Kind: attachJournalMessage, Text: "old"},
		{Kind: attachJournalCompaction, Text: "usable", Window: 2, HasWindow: true},
		{Kind: attachJournalCompaction, Text: "incomplete", HasWindow: false},
		{Kind: attachJournalMessage, Text: "new"},
	}

	compaction, tail := forkHistory(nil, journal)
	if compaction == nil || compaction.Text != "usable" {
		t.Fatalf("compaction = %+v, want usable", compaction)
	}
	want := []attachJournalRecord{journal[2], journal[3]}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("tail = %+v, want %+v", tail, want)
	}
}

func TestForkHistoryRespectsMetadataCutoff(t *testing.T) {
	journal := []attachJournalRecord{
		{Kind: attachJournalMessage, Text: "old"},
		{Kind: attachJournalCompaction, Text: "first", Window: 2, HasWindow: true},
		{Kind: attachJournalMessage, Text: "middle"},
		{Kind: attachJournalCompaction, Text: "newer", Window: 5, HasWindow: true},
		{Kind: attachJournalMessage, Text: "tail"},
	}
	metadata := session.WithCompactionCutoff(nil, 1)
	if got, ok := session.CompactionCutoff(metadata); !ok || got != 1 {
		t.Fatalf("CompactionCutoff = %d, %v, want 1, true", got, ok)
	}

	compaction, tail := forkHistory(metadata, journal)
	if compaction == nil || compaction.Text != "first" {
		t.Fatalf("compaction = %+v, want the cutoff record first", compaction)
	}
	want := []attachJournalRecord{journal[2], journal[3], journal[4]}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("tail = %+v, want %+v", tail, want)
	}
}

func TestForkHistoryMalformedCutoffFallsBackToFullScan(t *testing.T) {
	journal := []attachJournalRecord{
		{Kind: attachJournalCompaction, Text: "usable", Window: 1, HasWindow: true},
		{Kind: attachJournalMessage, Text: "tail"},
	}
	metadata := map[string]string{session.MetadataCompactionCutoff: "not-a-number"}
	if _, ok := session.CompactionCutoff(metadata); ok {
		t.Fatal("CompactionCutoff accepted a malformed value")
	}

	compaction, tail := forkHistory(metadata, journal)
	if compaction == nil || compaction.Text != "usable" {
		t.Fatalf("compaction = %+v, want usable from a full scan", compaction)
	}
	want := []attachJournalRecord{journal[1]}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("tail = %+v, want %+v", tail, want)
	}
}
