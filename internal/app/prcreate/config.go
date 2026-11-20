package prcreate

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// ProviderEnv is the environment override for the PR provider name. A flag
// passed to the command wins over it, and it wins over the project config.
const ProviderEnv = "BEACON_PR_PROVIDER"

// ResolveProvider picks a provider name with the precedence: explicit option,
// BEACON_PR_PROVIDER, <dir>/beacon.config.json, then DefaultProvider. A nil
// lookup skips the environment step; an optional reader supplies the config
// bytes, so this package never reads a file itself (ADR 0005). Any unreadable
// or malformed config falls back without failing the command.
func ResolveProvider(option, dir string, lookup func(string) string, readFile ...func(string) ([]byte, error)) string {
	if name := strings.TrimSpace(option); name != "" {
		return name
	}
	if lookup != nil {
		if name := strings.TrimSpace(lookup(ProviderEnv)); name != "" {
			return name
		}
	}
	if dir != "" && len(readFile) > 0 && readFile[0] != nil {
		raw, err := readFile[0](filepath.Join(dir, "beacon.config.json"))
		if err == nil {
			var cfg struct {
				PRProvider string `json:"prProvider"`
				Provider   string `json:"provider"`
			}
			if json.Unmarshal(raw, &cfg) == nil {
				if name := strings.TrimSpace(cfg.PRProvider); name != "" {
					return name
				}
				if name := strings.TrimSpace(cfg.Provider); name != "" {
					return name
				}
			}
		}
	}
	return DefaultProvider
}
