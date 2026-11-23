package batchapply

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteFileModeClassifiesSandboxDenial checks the denial path end to end:
// the writer output produces a classified error and one violation metric line.
func TestWriteFileModeClassifiesSandboxDenial(t *testing.T) {
	baseErr := errors.New("write failed")
	writerOut := "sandbox denied write: operation not permitted"
	var report bytes.Buffer
	r := &runner{
		opts:   Options{Quiet: true},
		output: output{out: &report},
		writeGate: writeGate{
			write: func(string, []byte) (string, error) {
				return writerOut, baseErr
			},
		},
	}

	err := r.writeFile(t.TempDir(), "docs/tasks/CCx-24.md", "body")
	var denial *sandboxDenialError
	if !errors.As(err, &denial) {
		t.Fatalf("error = %v, want *sandboxDenialError", err)
	}
	if denial.Output != writerOut {
		t.Errorf("Output = %q, want %q", denial.Output, writerOut)
	}
	if !errors.Is(err, baseErr) {
		t.Errorf("error does not wrap the write error: %v", err)
	}
	if r.sandboxDenials != 1 {
		t.Errorf("sandboxDenials = %d, want 1", r.sandboxDenials)
	}

	r.emitSandboxDenials()
	wantLine := fmt.Sprintf(sandboxDenialsFormat, 1)
	if got := report.String(); got != wantLine {
		t.Errorf("report = %q, want %q", got, wantLine)
	}
}

// TestWriteFileModeLeavesPermissionErrorUnclassified keeps a plain access
// error outside the denial type and leaves the violation metric at zero.
func TestWriteFileModeLeavesPermissionErrorUnclassified(t *testing.T) {
	baseErr := os.ErrPermission
	writerOut := "open /tmp/sandbox/docs/tasks/CCx-24.md: permission denied"
	var report bytes.Buffer
	r := &runner{
		opts:   Options{Quiet: true},
		output: output{out: &report},
		writeGate: writeGate{
			write: func(string, []byte) (string, error) {
				return writerOut, baseErr
			},
		},
	}

	err := r.writeFile(t.TempDir(), "docs/tasks/CCx-24.md", "body")
	var denial *sandboxDenialError
	if errors.As(err, &denial) {
		t.Fatalf("error = %v, want an ordinary file error", err)
	}
	if !errors.Is(err, baseErr) {
		t.Errorf("error = %v, want the original permission error", err)
	}
	if r.sandboxDenials != 0 {
		t.Errorf("sandboxDenials = %d, want 0", r.sandboxDenials)
	}

	r.emitSandboxDenials()
	if got := report.String(); got != "" {
		t.Errorf("report = %q, want no violation metric line", got)
	}
}

func TestIsLikelySandboxDenial(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "denial phrase", output: "sandbox denied write", want: true},
		{name: "reversed phrase", output: "write refused by sandbox", want: true},
		{name: "backend name", output: "landlock: denied", want: true},
		{name: "permission inside sandbox path", output: "open /tmp/sandbox/file: permission denied", want: false},
		{name: "plain access error", output: "permission denied", want: false},
		{name: "no output", output: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLikelySandboxDenial(tc.output); got != tc.want {
				t.Errorf("isLikelySandboxDenial(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

// TestWriteFileModeWritesOnSuccess keeps the production path unchanged when
// the injected writer succeeds, including the bytes passed to the writer.
func TestWriteFileModeWritesOnSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CCx-24.md")
	var written []byte
	r := &runner{
		opts:   Options{Quiet: true},
		output: output{out: &bytes.Buffer{}},
		writeGate: writeGate{
			write: func(gotPath string, data []byte) (string, error) {
				if gotPath != path {
					t.Errorf("path = %q, want %q", gotPath, path)
				}
				written = append([]byte(nil), data...)
				return "", nil
			},
		},
	}

	if err := r.writeFile(filepath.Dir(path), filepath.Base(path), "body"); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	if string(written) != "body" {
		t.Errorf("written = %q, want body", written)
	}
	if r.sandboxDenials != 0 {
		t.Errorf("sandboxDenials = %d, want 0", r.sandboxDenials)
	}
}
