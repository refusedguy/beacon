package main

import (
	"testing"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

func TestOTLPConfigFromArgsOverridesEnv(t *testing.T) {
	t.Setenv(observe.EnabledEnv, "true")
	t.Setenv(observe.ExporterEnv, "local")
	t.Setenv(observe.OTLPEndpointEnv, "http://env-collector:4318")
	t.Setenv(observe.OTLPProtocolEnv, "http/protobuf")

	cfg := otlpConfigFromArgs([]string{
		"--otel-exporter=otlp",
		"--otel-endpoint",
		"http://mock-collector:4318",
		"--otel-protocol=http/json",
	})
	if !cfg.Enabled {
		t.Fatal("enabled flag was lost")
	}
	if cfg.Exporter != "otlp" {
		t.Fatalf("exporter = %q", cfg.Exporter)
	}
	if cfg.Endpoint != "http://mock-collector:4318" {
		t.Fatalf("endpoint = %q", cfg.Endpoint)
	}
	if cfg.Protocol != observe.ProtocolHTTPJSON {
		t.Fatalf("protocol = %q", cfg.Protocol)
	}
}

func TestStripOTLPFlagsLeavesCommandFlagsIntact(t *testing.T) {
	in := []string{
		"daemon",
		"--otel-endpoint", "http://mock:4318",
		"--interval", "10s",
		"--otel-protocol=http/json",
		"--otel-exporter", "otlp",
		"--once",
	}
	want := []string{"daemon", "--interval", "10s", "--once"}
	got := stripOTLPFlags(in)
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestOTLPConfigFromArgsKeepsEnvWithoutFlags(t *testing.T) {
	t.Setenv(observe.EnabledEnv, "true")
	t.Setenv(observe.ExporterEnv, "otlp")
	t.Setenv(observe.OTLPEndpointEnv, "http://env-collector:4318")
	t.Setenv(observe.OTLPProtocolEnv, "http/protobuf")

	cfg := otlpConfigFromArgs([]string{"inspect", "--format", "json"})
	if cfg.Endpoint != "http://env-collector:4318" {
		t.Fatalf("endpoint = %q", cfg.Endpoint)
	}
	if cfg.Protocol != observe.ProtocolHTTPProtobuf {
		t.Fatalf("protocol = %q", cfg.Protocol)
	}
}
