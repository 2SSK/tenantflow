package logger

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestNewProductionJSON proves the production logger emits machine-readable
// JSON that we could feed to a log collector.
func TestNewProductionJSON(t *testing.T) {
	var buf bytes.Buffer
	l := New("production", &buf)

	l.Info("user created", "tenant", "42")

	// json.Unmarshal fails if the log line isn't valid JSON.
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("expected valid JSON log line, got %q: %v", buf.String(), err)
	}
	if m["msg"] != "user created" {
		t.Errorf("expected msg='user created', got %v", m["msg"])
	}
	if m["tenant"] != "42" {
		t.Errorf("expected tenant='42', got %v", m["tenant"])
	}
}

// TestNewDevelopmentText proves dev mode is human-readable, not JSON.
func TestNewDevelopmentText(t *testing.T) {
	var buf bytes.Buffer
	l := New("development", &buf)

	l.Info("user created", "tenant", "42")

	if !strings.Contains(buf.String(), "user created") {
		t.Errorf("expected human text containing message, got %q", buf.String())
	}
	if strings.Contains(buf.String(), `{"time"`) {
		t.Errorf("expected TEXT output in dev mode, got JSON: %q", buf.String())
	}
}

// TestColorWriterColorsErrorLine proves the wrapper paints ERROR lines.
func TestColorWriterColorsErrorLine(t *testing.T) {
	var buf bytes.Buffer
	cw := &colorWriter{w: &buf}

	cw.Write([]byte("level=ERROR msg=boom\n"))

	if !strings.Contains(buf.String(), colorRed) {
		t.Errorf("expected red ANSI code for ERROR line, got %q", buf.String())
	}
}

// TestIsTerminalFalseForBuffer proves buffers are NOT  treated as terminals
// - so a dev logger writing to a pipe/file stays clean.
func TestIsTerminalFalseForBuffer(t *testing.T) {
	var buf bytes.Buffer
	if isTerminal(&buf) {
		t.Errorf("expected bytes.Buffer to NOT be a terminal")
	}
}
