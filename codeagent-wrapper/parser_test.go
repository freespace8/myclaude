package main

import (
	"strings"
	"testing"
)

func TestBackendParseJSONStream_LargeSingleLineCodexEvent(t *testing.T) {
	payload := strings.Repeat("a", 10*1024*1024+1024)

	var sb strings.Builder
	sb.Grow(len(payload) + 128)
	sb.WriteString(`{"type":"item.completed","item":{"type":"agent_message","text":"`)
	sb.WriteString(payload)
	sb.WriteString(`"}}` + "\n")

	var warnings []string
	warnFn := func(msg string) { warnings = append(warnings, msg) }

	message, threadID := parseJSONStreamInternal(strings.NewReader(sb.String()), warnFn, nil, nil)

	if threadID != "" {
		t.Fatalf("threadID=%q, want empty", threadID)
	}
	if len(message) != len(payload) || message[0] != 'a' || message[len(message)-1] != 'a' {
		t.Fatalf("message_len=%d, want %d", len(message), len(payload))
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %q", warnings[0])
	}
}

func TestBackendParseJSONStream_EOFWithoutNewline(t *testing.T) {
	input := `{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`
	message, threadID := parseJSONStreamInternal(strings.NewReader(input), nil, nil, nil)
	if message != "hi" {
		t.Fatalf("message=%q, want hi", message)
	}
	if threadID != "" {
		t.Fatalf("threadID=%q, want empty", threadID)
	}
}

