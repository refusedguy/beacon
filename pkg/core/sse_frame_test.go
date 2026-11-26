package core

import (
	"errors"
	"strings"
	"testing"
)

func parseFrames(t *testing.T, stream string, eof SSEEOFPolicy) []SSEFrame {
	t.Helper()
	var frames []SSEFrame
	if err := ParseSSE(strings.NewReader(stream), eof, func(f SSEFrame) {
		frames = append(frames, f)
	}); err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	return frames
}

func TestParseSSEFrames(t *testing.T) {
	stream := ": heartbeat\r\n\r\nid: 4\r\nevent: run.updated\r\ndata: {\"id\":4}\r\ndata: {\"n\":5}\r\n\r\nid: 5\r\ndata: {\"type\":\"done\"}\r\n\r\n"
	frames := parseFrames(t, stream, SSEDropPartial)
	if len(frames) != 2 {
		t.Fatalf("frames: %d: %+v", len(frames), frames)
	}
	want := SSEFrame{ID: "4", Event: "run.updated", Data: "{\"id\":4}\n{\"n\":5}"}
	if frames[0] != want {
		t.Fatalf("first frame = %+v, want %+v", frames[0], want)
	}
	if frames[1].ID != "5" || frames[1].Data != "{\"type\":\"done\"}" {
		t.Fatalf("second frame = %+v", frames[1])
	}
}

func TestParseSSEDataLinesAcrossFrames(t *testing.T) {
	stream := strings.Join([]string{
		"data: one",
		"data: two",
		"data: three",
		"",
		"event: done",
		"data: four",
		"",
		"data: five",
		"",
	}, "\n")
	frames := parseFrames(t, stream, SSEKeepPartial)
	want := []SSEFrame{
		{Data: "one\ntwo\nthree"},
		{Event: "done", Data: "four"},
		{Data: "five"},
	}
	if len(frames) != len(want) {
		t.Fatalf("frames: %d: %+v", len(frames), frames)
	}
	for i := range want {
		if frames[i] != want[i] {
			t.Fatalf("frame %d = %+v, want %+v", i, frames[i], want[i])
		}
	}
}

func TestParseSSEEOFPolicy(t *testing.T) {
	stream := "id: 4\ndata: {\"type\":\"complete\"}\n\nid: 5\ndata: {\"type\":\"partial\"}"
	for _, tc := range []struct {
		name string
		eof  SSEEOFPolicy
		want int
	}{
		{"drop", SSEDropPartial, 1},
		{"keep", SSEKeepPartial, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := parseFrames(t, stream, tc.eof)
			if len(frames) != tc.want {
				t.Fatalf("frames: %d: %+v", len(frames), frames)
			}
			if frames[0].ID != "4" || !strings.Contains(frames[0].Data, "complete") {
				t.Fatalf("first frame = %+v", frames[0])
			}
			if tc.want == 2 && (frames[1].ID != "5" || frames[1].Data != "{\"type\":\"partial\"}") {
				t.Fatalf("second frame = %+v", frames[1])
			}
		})
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestParseSSEReturnsReadError(t *testing.T) {
	want := errors.New("read failed")
	err := ParseSSE(failingReader{err: want}, SSEKeepPartial, func(SSEFrame) {})
	if !errors.Is(err, want) {
		t.Fatalf("ParseSSE error = %v, want %v", err, want)
	}
}
