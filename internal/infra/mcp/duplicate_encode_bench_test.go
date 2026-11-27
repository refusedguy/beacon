package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/refusedguy/beacon/internal/app"
)

const (
	duplicateEncodePrefix      = "duplicate-mutation-encode"
	duplicateEncodeLimiterName = "mutex + O(1) LRU touch + json.Marshal bytes"
	duplicateEncodeLimiterSlug = "mutex-plus-o1-lru-touch-plus-json-marshal-bytes"
	duplicateEncodeCalls       = 200
)

// duplicateEncodeResult is one fixed task mutation payload. It is an
// OperationResult, so the duplicate path stamps it with the operation id and
// duplicate status through withMeta before jsonResult marshals it.
func duplicateEncodeResult() app.UpdateResult {
	return app.UpdateResult{
		Results: []app.UpdatedTask{
			{ID: "T-20261125-0001", Status: "updated"},
			{ID: "T-20261125-0002", Status: "unchanged"},
			{ID: "T-20261125-0003", Status: "not_found"},
		},
		Pushed: true,
	}
}

// newDuplicateEncodeJournal fills the journal with operationJournalLimit done
// ids and returns a batch of duplicate ids. Every batch id hits a done entry,
// so the measured calls serve cached results without running a mutation.
func newDuplicateEncodeJournal(t testing.TB) (*opJournal, []string) {
	t.Helper()
	j := newOpJournal()
	for i := 0; i < operationJournalLimit; i++ {
		id := fmt.Sprintf("op-%d", i)
		if _, err := j.run(context.Background(), id, func(context.Context) (any, error) {
			return duplicateEncodeResult(), nil
		}); err != nil {
			t.Fatalf("j.run(%q): %v", id, err)
		}
	}
	ids := make([]string, duplicateEncodeCalls)
	for i := range ids {
		ids[i] = fmt.Sprintf("op-%d", i%operationJournalLimit)
	}
	return j, ids
}

// printDuplicateEncodeReport writes the work, errs and named limiter line to
// stdout so a benchmark run reports them without -v.
func printDuplicateEncodeReport(w io.Writer, work, errs int64) {
	fmt.Fprintf(w, "%s: calls=%d work=%d errs=%d limiter=%s\n",
		duplicateEncodePrefix, int64(duplicateEncodeCalls), work, errs, duplicateEncodeLimiterName)
}

// BenchmarkDuplicateMutationEncode prices one duplicate task mutation through
// opJournal.run plus jsonResult, the two calls mutationTool makes on a retry.
// The journal is prefilled with 128 done ids; each pass serves 200 duplicates
// of a fixed result. The mutation fails the benchmark if it ever runs. Work is
// the number of duplicate results served, errs is the number of journal errors,
// and the limiter is the mutex, the O(1) LRU touch and the json.Marshal bytes.
// Every emitted payload is checked against json.Marshal of the same result.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1:
//
//	BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkDuplicateMutationEncode$' -benchtime=200x -count=5 -benchmem ./internal/infra/mcp/
func BenchmarkDuplicateMutationEncode(b *testing.B) {
	j, ids := newDuplicateEncodeJournal(b)
	mutation := func(context.Context) (any, error) {
		b.Fatal("duplicate ran the mutation again")
		return nil, nil
	}

	b.ReportAllocs()
	b.ResetTimer()
	var totalWork, totalErrs int64
	for i := 0; i < b.N; i++ {
		for _, id := range ids {
			result, err := j.run(context.Background(), id, mutation)
			if err != nil {
				totalErrs++
				continue
			}
			totalWork++
			got := resultWireBytes(b, jsonResult(result))
			want, err := json.Marshal(result)
			if err != nil {
				b.Fatalf("json.Marshal(result): %v", err)
			}
			if !bytes.Equal(got, want) {
				b.Fatalf("jsonResult changed the wire bytes\ngot  %s\nwant %s", got, want)
			}
		}
	}
	b.StopTimer()

	if b.N == 0 {
		b.Fatal("benchmark ran no passes")
	}
	if totalErrs != 0 {
		b.Fatalf("journal errors = %d, want zero", totalErrs)
	}
	passes := int64(b.N)
	work := totalWork / passes
	errs := totalErrs / passes

	printDuplicateEncodeReport(os.Stdout, work, errs)
	b.ReportMetric(float64(work), duplicateEncodePrefix+"-work")
	b.ReportMetric(float64(errs), duplicateEncodePrefix+"-errs")
	b.ReportMetric(1, duplicateEncodePrefix+"-limiter-"+duplicateEncodeLimiterSlug)
}
