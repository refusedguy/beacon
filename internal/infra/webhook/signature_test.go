package webhook

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// countingReader records how many bytes the caller consumed. An optional
// error makes it fail on the first read so tests can observe error wrapping.
type countingReader struct {
	data []byte
	err  error
	n    int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.n == len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.n:])
	r.n += n
	return n, nil
}

// repeatedReader supplies a long body without a giant fixture. It lets a test
// reach the 25 MB boundary by generating bytes as they are read.
type repeatedReader struct {
	remaining int64
	n         int64
}

func (r *repeatedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	for i := range p {
		p[i] = 'x'
	}
	n := len(p)
	r.remaining -= int64(n)
	r.n += int64(n)
	return n, nil
}

func TestReadBodyAtLimit(t *testing.T) {
	body := []byte("12345678")
	r := &countingReader{data: body}

	got, err := ReadBody(r, int64(len(body)))
	if err != nil {
		t.Fatalf("ReadBody at limit: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if r.n != len(body) {
		t.Fatalf("read %d bytes, want %d", r.n, len(body))
	}
}

func TestReadBodyOverLimit(t *testing.T) {
	const limit int64 = 8
	r := &countingReader{data: bytes.Repeat([]byte("x"), 1024)}

	got, err := ReadBody(r, limit)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("error = %v, want ErrBodyTooLarge", err)
	}
	if got != nil {
		t.Fatalf("body = %q, want nil on an oversized delivery", got)
	}
	if want := int(limit + 1); r.n != want {
		t.Fatalf("read %d bytes, want %d: ReadBody must not buffer the whole payload", r.n, want)
	}
}

func TestReadBodyNonPositiveLimitUsesDefault(t *testing.T) {
	for _, limit := range []int64{0, -1} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			short := []byte("short delivery")
			got, err := ReadBody(&countingReader{data: short}, limit)
			if err != nil {
				t.Fatalf("ReadBody short body: %v", err)
			}
			if !bytes.Equal(got, short) {
				t.Fatalf("body = %q, want %q", got, short)
			}

			r := &repeatedReader{remaining: MaxBodyBytes + 1}
			got, err = ReadBody(r, limit)
			if !errors.Is(err, ErrBodyTooLarge) {
				t.Fatalf("error = %v, want ErrBodyTooLarge", err)
			}
			if got != nil {
				t.Fatalf("body = %q, want nil at the default limit", got)
			}
			if r.n != MaxBodyBytes+1 {
				t.Fatalf("read %d bytes, want %d", r.n, MaxBodyBytes+1)
			}
		})
	}
}

func TestReadBodyNilReader(t *testing.T) {
	got, err := ReadBody(nil, 8)
	if err != nil {
		t.Fatalf("ReadBody(nil): %v", err)
	}
	if got != nil {
		t.Fatalf("body = %q, want nil for a nil reader", got)
	}
}

func TestReadBodyReaderError(t *testing.T) {
	wantErr := errors.New("connection reset")
	r := &countingReader{err: wantErr}

	got, err := ReadBody(r, 8)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "read webhook body") {
		t.Fatalf("error = %q, want 'read webhook body' context", err)
	}
	if got != nil {
		t.Fatalf("body = %q, want nil on a read failure", got)
	}
}
