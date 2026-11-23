package main

import "testing"

func TestParseRemote(t *testing.T) {
	cases := []struct {
		raw      string
		provider string
		owner    string
		repo     string
	}{
		{"git@github.com:acme/demo.git", "github", "acme", "demo"},
		{"https://github.com/acme/demo.git", "github", "acme", "demo"},
		{"ssh://git@gitlab.com/acme/demo.git", "gitlab", "acme", "demo"},
		{"ssh://git@gitlab.example.com:2222/acme/demo.git", "gitlab", "acme", "demo"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			r, err := parseRemote(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if r.provider != tc.provider || r.owner != tc.owner || r.repo != tc.repo {
				t.Fatalf("got %+v", r)
			}
		})
	}
}
