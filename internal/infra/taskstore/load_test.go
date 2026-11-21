package taskstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

func TestFileAdapterReadsCounterGrowsAndResets(t *testing.T) {
	previous := observe.Default()
	defer observe.SetDefault(previous)

	m := observe.NewMetrics()
	observe.SetDefault(observe.NewRecorder(nil, m))

	path := filepath.Join(t.TempDir(), "card.md")
	if err := os.WriteFile(path, []byte("card"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := FileAdapter{}
	for i := 0; i < 3; i++ {
		data, err := adapter.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "card" {
			t.Fatalf("ReadFile = %q, ожидалось card", data)
		}
	}
	if got := counterValue(t, m, observe.CounterFileAdapterReads); got != 3 {
		t.Errorf("%s = %d, ожидалось 3", observe.CounterFileAdapterReads, got)
	}
	if text := string(m.Prometheus()); !strings.Contains(text, "beacon_file_adapter_reads_total 3") {
		t.Errorf("в Prometheus-выводе нет beacon_file_adapter_reads_total 3:\n%s", text)
	}

	m.Reset()
	if got := counterValue(t, m, observe.CounterFileAdapterReads); got != 0 {
		t.Errorf("после Reset %s = %d, ожидалось 0", observe.CounterFileAdapterReads, got)
	}
}

func counterValue(t *testing.T, m *observe.Metrics, name string) uint64 {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(m.JSON(), &got); err != nil {
		t.Fatal(err)
	}
	value, ok := got[name].(float64)
	if !ok {
		t.Fatalf("счётчик %s = %v, ожидалось число", name, got[name])
	}
	return uint64(value)
}
