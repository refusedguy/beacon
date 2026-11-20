package registry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secretValue is a distinctive value that must never appear in an error. If
// it ever does, the guard has leaked the secret into a log line.
const secretValue = "s3cr3t-DO-NOT-LOG-9f8e7d6c"

func TestGuardConfigCleanConfigPasses(t *testing.T) {
	configs := []string{
		`{}`,
		`{"host":"registry.example.com","port":8080,"retries":3}`,
		`{"adapters":["opencode","acp"],"settings":{"enabled":true}}`,
		`{"keyboard":"layout","monkey":"animal","keyword":"search"}`,
	}
	for _, config := range configs {
		if err := GuardConfig([]byte(config)); err != nil {
			t.Errorf("GuardConfig(%s) = %v, want nil", config, err)
		}
	}
}

func TestGuardConfigRejectsSecretFields(t *testing.T) {
	tests := []struct {
		name      string
		config    string
		wantField string
	}{
		{"token", `{"host":"x","token":"` + secretValue + `"}`, "token"},
		{"key", `{"key":"` + secretValue + `"}`, "key"},
		{"password", `{"password":"` + secretValue + `"}`, "password"},
		{"auth.json field", `{"auth.json":"` + secretValue + `"}`, "auth.json"},
		{"snake compound", `{"api_key":"` + secretValue + `"}`, "key"},
		{"camel compound", `{"privateToken":"` + secretValue + `"}`, "token"},
		{"all caps", `{"AUTH_TOKEN":"` + secretValue + `"}`, "token"},
		{"kebab compound", `{"db-password":"` + secretValue + `"}`, "password"},
		{"auth.json value reference", `{"credentialFile":"auth.json"}`, "auth.json"},
		{"nested", `{"outer":{"inner":{"password":"` + secretValue + `"}}}`, "password"},
		{"array", `{"items":[{"ok":true},{"token":"` + secretValue + `"}]}`, "token"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := GuardConfig([]byte(tc.config))
			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("GuardConfig error = %v, want *ConfigError", err)
			}
			if ce.Field != tc.wantField {
				t.Errorf("Field = %q, want %q", ce.Field, tc.wantField)
			}
			// Проверка: обнаруженное поле называет строку, не значение.
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("error %q does not name field %q", err, tc.wantField)
			}
			// Приёмка: содержимое секрета не попадает в лог.
			if strings.Contains(err.Error(), secretValue) {
				t.Errorf("error %q leaked the secret value", err)
			}
		})
	}
}

func TestGuardConfigPriorityIsDeterministic(t *testing.T) {
	// Several markers in one config: the guard must report the same one on
	// every call regardless of Go's random map iteration order.
	config := []byte(`{"token":"` + secretValue + `","password":"` + secretValue + `"}`)
	want := "token"
	for i := 0; i < 20; i++ {
		var ce *ConfigError
		if err := GuardConfig(config); !errors.As(err, &ce) || ce.Field != want {
			t.Fatalf("call %d: GuardConfig = %v, want field %q", i, err, want)
		}
	}
}

func TestGuardConfigSizeLimit(t *testing.T) {
	// A body over the ceiling is rejected on size before JSON parsing, even
	// when it is not valid JSON.
	oversized := append([]byte(`{"data":"`), make([]byte, maxConfigBytes)...)
	if err := GuardConfig(oversized); err == nil {
		t.Fatal("GuardConfig accepted an oversized body")
	} else {
		var ce *ConfigError
		if !errors.As(err, &ce) {
			t.Fatalf("error = %v, want *ConfigError", err)
		}
		if ce.Field != "size" {
			t.Errorf("Field = %q, want size", ce.Field)
		}
		if strings.Contains(err.Error(), string(oversized)) {
			t.Error("size error echoed the oversized body")
		}
	}

	// A body exactly at the ceiling is accepted.
	prefix := `{"data":"`
	suffix := `"}`
	filler := strings.Repeat("a", maxConfigBytes-len(prefix)-len(suffix))
	atLimit := prefix + filler + suffix
	if len(atLimit) != maxConfigBytes {
		t.Fatalf("test fixture length = %d, want %d", len(atLimit), maxConfigBytes)
	}
	if err := GuardConfig([]byte(atLimit)); err != nil {
		t.Errorf("GuardConfig at size limit = %v, want nil", err)
	}
}

func TestGuardConfigRejectsMalformedJSON(t *testing.T) {
	// The decoder error must not be wrapped into the returned message, so a
	// fragment of a bad value cannot reach a log line.
	bad := []byte(`{"token":` + secretValue + `}`)
	err := GuardConfig(bad)
	if err == nil {
		t.Fatal("GuardConfig accepted malformed JSON")
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Errorf("error %q leaked a fragment of the value", err)
	}
	if strings.Contains(err.Error(), "invalid JSON") == false {
		t.Errorf("error %q does not say JSON is invalid", err)
	}
}

func TestGuardFile(t *testing.T) {
	dir := t.TempDir()

	clean := filepath.Join(dir, "clean.json")
	if err := os.WriteFile(clean, []byte(`{"host":"x"}`), 0o600); err != nil {
		t.Fatalf("write clean config: %v", err)
	}
	if err := GuardFile(clean); err != nil {
		t.Errorf("GuardFile(clean) = %v, want nil", err)
	}

	withSecret := filepath.Join(dir, "author.json")
	if err := os.WriteFile(withSecret, []byte(`{"password":"`+secretValue+`"}`), 0o600); err != nil {
		t.Fatalf("write author config: %v", err)
	}
	err := GuardFile(withSecret)
	var ce *ConfigError
	if !errors.As(err, &ce) || ce.Field != "password" {
		t.Fatalf("GuardFile(author) = %v, want field password", err)
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Errorf("GuardFile error %q leaked the secret value", err)
	}

	large := filepath.Join(dir, "large.json")
	if err := os.WriteFile(large, make([]byte, maxConfigBytes+1), 0o600); err != nil {
		t.Fatalf("write large config: %v", err)
	}
	err = GuardFile(large)
	if !errors.As(err, &ce) || ce.Field != "size" {
		t.Fatalf("GuardFile(large) = %v, want field size", err)
	}

	if err := GuardFile(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("GuardFile(missing) = nil, want error")
	}
}
