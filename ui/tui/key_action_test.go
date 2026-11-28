package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestKeyToActionWiring pins the action each control key sends. The wire body
// is recorded by the loopback server, so swapping two keys fails the assertion
// even if both actions would have been valid on their own.
func TestKeyToActionWiring(t *testing.T) {
	var mu sync.Mutex
	var posted []Action
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiControlPath:
			var req ControlRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			mu.Lock()
			posted = append(posted, req.Action)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"action":"`+string(req.Action)+`","run_id":"run-key","applied":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cases := []struct {
		key     string
		want    Action
		presses int
	}{
		{"a", ActionApprove, 1},
		{"u", ActionUnblock, 1},
		{"c", ActionCancel, 2},
		{"x", ActionCancel, 2},
		{"r", ActionRetry, 2},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			mu.Lock()
			posted = nil
			mu.Unlock()

			m := newModel(NewClient(srv.URL, "tok"))
			m.runs = []Run{{ID: "run-key", State: "ready", HeadSHA: "abc"}}
			m.cursor = 0
			message := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)}

			var current tea.Model = m
			var cmd tea.Cmd
			for press := 1; press <= tc.presses; press++ {
				next, produced := current.Update(message)
				current = next
				cmd = produced
				if press < tc.presses {
					if cmd != nil {
						t.Fatalf("press %d of %q armed a command before confirmation", press, tc.key)
					}
					if armed := next.(model).pending; armed != tc.want {
						t.Fatalf("pending after %q = %q, want %q", tc.key, armed, tc.want)
					}
				}
			}
			if cmd == nil {
				t.Fatalf("key %q produced no control command", tc.key)
			}
			raw := cmd()
			msg, ok := raw.(actionMsg)
			if !ok {
				t.Fatalf("key %q returned %T, want actionMsg", tc.key, raw)
			}
			if msg.action != tc.want {
				t.Fatalf("key %q produced action %q, want %q", tc.key, msg.action, tc.want)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(posted) != 1 || posted[0] != tc.want {
				t.Fatalf("key %q posted actions %v, want [%s]", tc.key, posted, tc.want)
			}
		})
	}
}
