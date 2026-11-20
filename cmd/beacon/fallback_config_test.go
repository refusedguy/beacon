package main

import "testing"

// TestResolveFallbackConfigWithFile covers the case where a config file is
// present. Only the keys the file sets change; every other value comes from
// the built-in layer.
func TestResolveFallbackConfigWithFile(t *testing.T) {
	got := resolveFallbackConfig("/repo/.beacon/config.yaml",
		[]byte("maxRebases: 7\nslotsTotal: 6\n"))
	if got.Origin != configFromFile {
		t.Fatalf("origin = %q, want %q", got.Origin, configFromFile)
	}
	if got.Config.MaxRebases != 7 || got.Config.SlotsTotal != 6 {
		t.Fatalf("config = %+v, want maxRebases 7 and slotsTotal 6", got.Config)
	}
}

// TestResolveFallbackConfigPartialOverlay proves comments, quotes, inline
// comments and keys owned by other consumers do not disturb the limits.
func TestResolveFallbackConfigPartialOverlay(t *testing.T) {
	got := resolveFallbackConfig("/repo/.beacon/config.yaml", []byte(
		"# shared limits\nmax-rebases: '5' # from the project\nrepo: acme/widgets\n"))
	want := fallbackConfig{MaxRebases: 5, SlotsTotal: fallbackDefaultSlotsTotal}
	if got.Origin != configFromFile || got.Config != want {
		t.Fatalf("resolved = %+v, want config %+v from file", got, want)
	}
}

// TestResolveFallbackConfigMissingFile covers an absent config.yaml. The
// caller passes nil bytes when the file is not there; the run still gets
// predictable limits and does not fail.
func TestResolveFallbackConfigMissingFile(t *testing.T) {
	got := resolveFallbackConfig("/repo/.beacon/config.yaml", nil)
	want := resolvedConfig{Config: fallbackConfigDefaults(), Origin: configFromDefaults}
	if got != want {
		t.Fatalf("resolved = %+v, want defaults %+v", got, want)
	}
}

// TestResolveFallbackConfigBrokenFile covers malformed content. Every unusable
// file leaves the defaults in place, so a broken config cannot stop a workflow
// pass.
func TestResolveFallbackConfigBrokenFile(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "no colon", content: "maxRebases 7\n"},
		{name: "not a number", content: "maxRebases: seven\n"},
		{name: "above range", content: "maxRebases: 99\n"},
		{name: "below range", content: "maxRebases: 0\n"},
		{name: "negative slots", content: "slotsTotal: -1\n"},
		{name: "valid key after broken key", content: "maxRebases: seven\nslotsTotal: 6\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveFallbackConfig("/repo/.beacon/config.yaml", []byte(tc.content))
			want := resolvedConfig{Config: fallbackConfigDefaults(), Origin: configFromDefaults}
			if got != want {
				t.Fatalf("resolved = %+v, want defaults %+v", got, want)
			}
		})
	}
}

// TestResolveFallbackConfigEmptyPath covers a caller with no project root. The
// file layer is skipped even when bytes are supplied.
func TestResolveFallbackConfigEmptyPath(t *testing.T) {
	for _, path := range []string{"", "   "} {
		got := resolveFallbackConfig(path, []byte("maxRebases: 7\nslotsTotal: 6\n"))
		want := resolvedConfig{Config: fallbackConfigDefaults(), Origin: configFromDefaults}
		if got != want {
			t.Fatalf("path %q: resolved = %+v, want defaults %+v", path, got, want)
		}
	}
}

// TestResolveFallbackConfigDefaultsAreFresh proves the resolver returns values
// rather than a shared instance. Mutating one result cannot change the next.
func TestResolveFallbackConfigDefaultsAreFresh(t *testing.T) {
	first := resolveFallbackConfig("", nil)
	first.Config.MaxRebases = 99
	second := resolveFallbackConfig("", nil)
	if second.Config.MaxRebases != fallbackDefaultMaxRebases {
		t.Fatalf("defaults leaked between calls: %+v", second.Config)
	}
}
