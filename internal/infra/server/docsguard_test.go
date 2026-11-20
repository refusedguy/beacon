package server

import (
	"errors"
	"strings"
	"testing"
)

// stubSource is the mock port for Check. It hands back fixed maps and an
// optional document error, so the tests never touch the filesystem and never
// import a sibling infra package.
type stubSource struct {
	document map[string]int
	code     map[string]int
	docErr   error
}

func (s stubSource) DocumentNumbers() (map[string]int, error) { return s.document, s.docErr }
func (s stubSource) CodeNumbers() map[string]int              { return s.code }

// agreeingNumbers returns a document and a code snapshot that match on every
// canonical key. Tests mutate one side to create drift.
func agreeingNumbers() (map[string]int, map[string]int) {
	document := map[string]int{
		KeyMCPTools:           7,
		KeyEventSchemaVersion: 1,
		KeyReplayPageSize:     500,
	}
	code := map[string]int{
		KeyMCPTools:           7,
		KeyEventSchemaVersion: 1,
		KeyReplayPageSize:     500,
	}
	return document, code
}

func TestCheckMatchingNumbersOK(t *testing.T) {
	document, code := agreeingNumbers()
	mismatches, err := Check(stubSource{document: document, code: code})
	if err != nil {
		t.Fatalf("Check = %v, want nil error", err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("Check mismatches = %v, want none", mismatches)
	}
}

func TestCheckDetectsToolCountDrift(t *testing.T) {
	// Приёмка: изменение числа инструментов ломает guard.
	document, code := agreeingNumbers()
	document[KeyMCPTools] = 8

	mismatches, err := Check(stubSource{document: document, code: code})
	if err != nil {
		t.Fatalf("Check = %v, want nil error", err)
	}
	if len(mismatches) != 1 {
		t.Fatalf("Check mismatches = %v, want exactly one", mismatches)
	}
	want := Mismatch{Key: KeyMCPTools, Document: 8, Code: 7}
	if mismatches[0] != want {
		t.Fatalf("mismatch = %+v, want %+v", mismatches[0], want)
	}
	if !strings.Contains(mismatches[0].String(), "mcp_tools") {
		t.Errorf("mismatch String() = %q, want it to name mcp_tools", mismatches[0])
	}
}

func TestCheckMissingDocumentHandled(t *testing.T) {
	// Отсутствующий файл обработан: sentinel-ошибка различима, паники нет,
	// расхождений не выдаётся.
	_, code := agreeingNumbers()
	mismatches, err := Check(stubSource{code: code, docErr: ErrDocumentMissing})
	if !errors.Is(err, ErrDocumentMissing) {
		t.Fatalf("Check error = %v, want errors.Is(err, ErrDocumentMissing)", err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("Check mismatches = %v, want none for a missing document", mismatches)
	}
}

func TestCheckMissingKeyIsDrift(t *testing.T) {
	document, code := agreeingNumbers()
	delete(document, KeyReplayPageSize)

	mismatches, err := Check(stubSource{document: document, code: code})
	if err != nil {
		t.Fatalf("Check = %v, want nil error", err)
	}
	if len(mismatches) != 1 {
		t.Fatalf("Check mismatches = %v, want exactly one", mismatches)
	}
	want := Mismatch{Key: KeyReplayPageSize, Code: 500, Missing: "document"}
	if mismatches[0] != want {
		t.Fatalf("mismatch = %+v, want %+v", mismatches[0], want)
	}
}

func TestCheckDeterministicOrder(t *testing.T) {
	document, code := agreeingNumbers()
	document[KeyMCPTools] = 8
	document[KeyEventSchemaVersion] = 2
	document[KeyReplayPageSize] = 501

	for i := 0; i < 20; i++ {
		mismatches, err := Check(stubSource{document: document, code: code})
		if err != nil {
			t.Fatalf("call %d: Check = %v", i, err)
		}
		if len(mismatches) != len(Keys) {
			t.Fatalf("call %d: mismatches = %v, want %d", i, mismatches, len(Keys))
		}
		for j, key := range Keys {
			if mismatches[j].Key != key {
				t.Fatalf("call %d: mismatch %d key = %q, want %q", i, j, mismatches[j].Key, key)
			}
		}
	}
}

func TestCheckWrapsSourceError(t *testing.T) {
	boom := errors.New("permission denied")
	_, code := agreeingNumbers()
	mismatches, err := Check(stubSource{code: code, docErr: boom})
	if !errors.Is(err, boom) {
		t.Fatalf("Check error = %v, want it to wrap the source error", err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("Check mismatches = %v, want none on a source error", mismatches)
	}
}

func TestReport(t *testing.T) {
	if got := Report(nil); got != "" {
		t.Errorf("Report(nil) = %q, want empty", got)
	}
	document, code := agreeingNumbers()
	document[KeyMCPTools] = 8
	mismatches, err := Check(stubSource{document: document, code: code})
	if err != nil {
		t.Fatalf("Check = %v", err)
	}
	got := Report(mismatches)
	if !strings.Contains(got, "mcp_tools") || !strings.Contains(got, "8") || !strings.Contains(got, "7") {
		t.Errorf("Report = %q, want it to show the drifted number", got)
	}
}
