package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// versionedToolsList builds a server with an explicit version, walks its
// tools/list and returns the tools together with everything the core wrote to
// stdout. The in-memory transport means the core never needs a real stdio.
func versionedToolsList(t *testing.T, version string) ([]*sdkmcp.Tool, string) {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	original := os.Stdout
	os.Stdout = writer

	var tools []*sdkmcp.Tool
	func() {
		defer func() {
			os.Stdout = original
			_ = writer.Close()
		}()

		server := NewServerWithDeps(Dependencies{Version: version})
		client := sdkmcp.NewClient(
			&sdkmcp.Implementation{Name: "beacon-version-test", Version: "v0.0.0"},
			nil,
		)
		serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
		serverSession, err := server.Connect(context.Background(), serverTransport, nil)
		if err != nil {
			t.Fatalf("connect server: %v", err)
		}
		clientSession, err := client.Connect(context.Background(), clientTransport, nil)
		if err != nil {
			t.Fatalf("connect client: %v", err)
		}
		defer func() {
			_ = clientSession.Close()
			_ = serverSession.Wait()
		}()

		cursor := ""
		seen := make(map[string]bool)
		for {
			params := &sdkmcp.ListToolsParams{}
			if cursor != "" {
				params.Cursor = cursor
			}
			response, err := clientSession.ListTools(context.Background(), params)
			if err != nil {
				t.Fatalf("tools/list: %v", err)
			}
			tools = append(tools, response.Tools...)
			if response.NextCursor == "" {
				break
			}
			if seen[response.NextCursor] {
				t.Fatalf("tools/list pagination loop at cursor %q", response.NextCursor)
			}
			seen[response.NextCursor] = true
			cursor = response.NextCursor
		}
	}()

	captured, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	_ = reader.Close()
	return tools, string(captured)
}

// toolJSONWithoutDescription serializes a tool with its description blanked.
// It lets the test prove that a version changes no other part of the tool.
func toolJSONWithoutDescription(t *testing.T, tool *sdkmcp.Tool) []byte {
	t.Helper()
	copy := *tool
	copy.Description = ""
	b, err := json.Marshal(copy)
	if err != nil {
		t.Fatalf("marshal tool %q: %v", tool.Name, err)
	}
	return b
}

// TestToolsListVersionIsDynamic proves that two build versions produce two
// different descriptions while the rest of the listing stays byte-identical.
// It also checks that the core writes nothing to stdout.
func TestToolsListVersionIsDynamic(t *testing.T) {
	first, firstStdout := versionedToolsList(t, "v1.2.3")
	second, secondStdout := versionedToolsList(t, "v4.5.6")

	if firstStdout != "" || secondStdout != "" {
		t.Fatalf("core wrote to stdout: first %q, second %q", firstStdout, secondStdout)
	}
	if len(first) != len(ToolNames) || len(second) != len(first) {
		t.Fatalf("tools/list sizes = %d and %d, want %d", len(first), len(second), len(ToolNames))
	}

	versioned := 0
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Fatalf("tool order changed at %d: %q vs %q", i, first[i].Name, second[i].Name)
		}
		if !bytes.Equal(toolJSONWithoutDescription(t, first[i]), toolJSONWithoutDescription(t, second[i])) {
			t.Errorf("version changed a field other than the description of %q", first[i].Name)
		}
		if first[i].Name != "beacon_stats" {
			if first[i].Description != second[i].Description {
				t.Errorf("description of %q changed with the build version", first[i].Name)
			}
			continue
		}
		versioned++
		if !strings.Contains(first[i].Description, "v1.2.3") || !strings.Contains(second[i].Description, "v4.5.6") {
			t.Errorf("beacon_stats description does not carry its build version: %q / %q", first[i].Description, second[i].Description)
		}
		if first[i].Description == second[i].Description {
			t.Error("two build versions produced the same beacon_stats description")
		}
		if strings.Contains(first[i].Description, "v4.5.6") || strings.Contains(second[i].Description, "v1.2.3") {
			t.Error("beacon_stats description mixed two build versions")
		}
	}
	if versioned != 1 {
		t.Errorf("tools carrying a build version = %d, want 1", versioned)
	}
}

// TestUnstampedBuildKeepsDescriptionStable proves that a source build without
// a version stamp leaves the description untouched. The pinned tools/list
// contract in contract_test.go depends on this.
func TestUnstampedBuildKeepsDescriptionStable(t *testing.T) {
	tools, stdout := versionedToolsList(t, "")
	if stdout != "" {
		t.Fatalf("core wrote to stdout: %q", stdout)
	}
	for _, tool := range tools {
		if tool.Name == "beacon_stats" && strings.Contains(tool.Description, "Version:") {
			t.Fatalf("unstamped build appended a version: %q", tool.Description)
		}
	}
}

// TestBuildVersionPrefersBuildStamp proves that the value injected by the
// build is the source BuildVersion reports, ahead of the Go build info.
func TestBuildVersionPrefersBuildStamp(t *testing.T) {
	original := versionFromBuild
	defer func() { versionFromBuild = original }()

	versionFromBuild = "v7.8.9"
	if got := BuildVersion(); got != "v7.8.9" {
		t.Errorf("BuildVersion() = %q, want %q", got, "v7.8.9")
	}
}
