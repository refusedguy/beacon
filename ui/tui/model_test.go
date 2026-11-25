package main

import (
	"net/http"
	"testing"
	"unicode/utf8"
)

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{"ascii unchanged", "abcdef", 10, "abcdef"},
		{"ascii clipped", "abcdef", 4, "abc…"},
		{"multibyte fits by rune count", "ééé", 4, "ééé"},
		{"cjk clipped", "日本語テスト", 4, "日本語…"},
		{"emoji clipped", "a😀b😀c", 4, "a😀b…"},
		{"cjk then ascii clipped", "界ab", 2, "界…"},
		{"non-positive limit", "界", 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.input, tc.limit)
			if got != tc.want {
				t.Fatalf("truncate(%q, %d) = %q, want %q", tc.input, tc.limit, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncate(%q, %d) produced invalid UTF-8: %q", tc.input, tc.limit, got)
			}
		})
	}
}

func TestStreamHTTPClientIsFresh(t *testing.T) {
	transport := &http.Transport{}
	base := &http.Client{Timeout: requestTimeout, Transport: transport}
	c := &Client{HTTP: base}

	streamClient := c.streamHTTPClient()
	if streamClient == base {
		t.Fatal("stream client must be a fresh *http.Client, not the base")
	}
	if streamClient.Timeout != 0 {
		t.Fatalf("stream client timeout = %v, want 0", streamClient.Timeout)
	}
	if streamClient.Transport != transport {
		t.Fatal("stream client must reuse the configured transport")
	}
	if again := c.streamHTTPClient(); again == streamClient {
		t.Fatal("each call must return a fresh client")
	}
}
