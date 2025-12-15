package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// JSONEvent represents a Codex JSON output event
type JSONEvent struct {
	Type     string     `json:"type"`
	ThreadID string     `json:"thread_id,omitempty"`
	Item     *EventItem `json:"item,omitempty"`
}

// EventItem represents the item field in a JSON event
type EventItem struct {
	Type string      `json:"type"`
	Text interface{} `json:"text"`
}

// ClaudeEvent for Claude stream-json format
type ClaudeEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Result    string `json:"result,omitempty"`
}

// GeminiEvent for Gemini stream-json format
type GeminiEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	Role      string `json:"role,omitempty"`
	Content   string `json:"content,omitempty"`
	Delta     bool   `json:"delta,omitempty"`
	Status    string `json:"status,omitempty"`
}

func parseJSONStream(r io.Reader) (message, threadID string) {
	return parseJSONStreamWithLog(r, logWarn, logInfo)
}

func parseJSONStreamWithWarn(r io.Reader, warnFn func(string)) (message, threadID string) {
	return parseJSONStreamWithLog(r, warnFn, logInfo)
}

func parseJSONStreamWithLog(r io.Reader, warnFn func(string), infoFn func(string)) (message, threadID string) {
	return parseJSONStreamInternal(r, warnFn, infoFn, nil)
}

func parseJSONStreamInternal(r io.Reader, warnFn func(string), infoFn func(string), onMessage func()) (message, threadID string) {
	if warnFn == nil {
		warnFn = func(string) {}
	}
	if infoFn == nil {
		infoFn = func(string) {}
	}

	notifyMessage := func() {
		if onMessage != nil {
			onMessage()
		}
	}

	totalEvents := 0

	var (
		codexMessage  string
		claudeMessage string
		geminiBuffer  strings.Builder
	)

	truncateBytes := func(b []byte, maxLen int) string {
		if len(b) <= maxLen {
			return string(b)
		}
		if maxLen < 0 {
			return ""
		}
		return string(b[:maxLen]) + "..."
	}

	processLine := func(line []byte) {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			warnFn(fmt.Sprintf("Failed to parse line: %s", truncateBytes(line, 100)))
			return
		}

		hasItemType := false
		if rawItem, ok := raw["item"]; ok {
			var itemMap map[string]json.RawMessage
			if err := json.Unmarshal(rawItem, &itemMap); err == nil {
				if _, ok := itemMap["type"]; ok {
					hasItemType = true
				}
			}
		}

		isCodex := hasItemType
		if !isCodex {
			if _, ok := raw["thread_id"]; ok {
				isCodex = true
			}
		}

		switch {
		case isCodex:
			var event JSONEvent
			if err := json.Unmarshal(line, &event); err != nil {
				warnFn(fmt.Sprintf("Failed to parse Codex event: %s", truncateBytes(line, 100)))
				return
			}

			var details []string
			if event.ThreadID != "" {
				details = append(details, fmt.Sprintf("thread_id=%s", event.ThreadID))
			}
			if event.Item != nil && event.Item.Type != "" {
				details = append(details, fmt.Sprintf("item_type=%s", event.Item.Type))
			}
			if len(details) > 0 {
				infoFn(fmt.Sprintf("Parsed event #%d type=%s (%s)", totalEvents, event.Type, strings.Join(details, ", ")))
			} else {
				infoFn(fmt.Sprintf("Parsed event #%d type=%s", totalEvents, event.Type))
			}

			switch event.Type {
			case "thread.started":
				threadID = event.ThreadID
				infoFn(fmt.Sprintf("thread.started event thread_id=%s", threadID))
			case "item.completed":
				var itemType string
				var normalized string
				if event.Item != nil {
					itemType = event.Item.Type
					normalized = normalizeText(event.Item.Text)
				}
				infoFn(fmt.Sprintf("item.completed event item_type=%s message_len=%d", itemType, len(normalized)))
				if event.Item != nil && event.Item.Type == "agent_message" && normalized != "" {
					codexMessage = normalized
					notifyMessage()
				}
			}

		case hasKey(raw, "subtype") || hasKey(raw, "result"):
			var event ClaudeEvent
			if err := json.Unmarshal(line, &event); err != nil {
				warnFn(fmt.Sprintf("Failed to parse Claude event: %s", truncateBytes(line, 100)))
				return
			}

			if event.SessionID != "" && threadID == "" {
				threadID = event.SessionID
			}

			infoFn(fmt.Sprintf("Parsed Claude event #%d type=%s subtype=%s result_len=%d", totalEvents, event.Type, event.Subtype, len(event.Result)))

			if event.Result != "" {
				claudeMessage = event.Result
				notifyMessage()
			}

		case hasKey(raw, "role") || hasKey(raw, "delta"):
			var event GeminiEvent
			if err := json.Unmarshal(line, &event); err != nil {
				warnFn(fmt.Sprintf("Failed to parse Gemini event: %s", truncateBytes(line, 100)))
				return
			}

			if event.SessionID != "" && threadID == "" {
				threadID = event.SessionID
			}

			if event.Content != "" {
				geminiBuffer.WriteString(event.Content)
				notifyMessage()
			}

			infoFn(fmt.Sprintf("Parsed Gemini event #%d type=%s role=%s delta=%t status=%s content_len=%d", totalEvents, event.Type, event.Role, event.Delta, event.Status, len(event.Content)))

		default:
			warnFn(fmt.Sprintf("Unknown event format: %s", truncateBytes(line, 100)))
		}
	}

	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')

		if len(line) > 0 {
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				totalEvents++
				processLine(line)
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			warnFn("Read stdout error: " + err.Error())
			break
		}
	}

	switch {
	case geminiBuffer.Len() > 0:
		message = geminiBuffer.String()
	case claudeMessage != "":
		message = claudeMessage
	default:
		message = codexMessage
	}

	infoFn(fmt.Sprintf("parseJSONStream completed: events=%d, message_len=%d, thread_id_found=%t", totalEvents, len(message), threadID != ""))
	return message, threadID
}

func hasKey(m map[string]json.RawMessage, key string) bool {
	_, ok := m[key]
	return ok
}

func discardInvalidJSON(decoder *json.Decoder, reader *bufio.Reader) (*bufio.Reader, error) {
	var buffered bytes.Buffer

	if decoder != nil {
		if buf := decoder.Buffered(); buf != nil {
			_, _ = buffered.ReadFrom(buf)
		}
	}

	line, err := reader.ReadBytes('\n')
	buffered.Write(line)

	data := buffered.Bytes()
	newline := bytes.IndexByte(data, '\n')
	if newline == -1 {
		return reader, err
	}

	remaining := data[newline+1:]
	if len(remaining) == 0 {
		return reader, err
	}

	return bufio.NewReader(io.MultiReader(bytes.NewReader(remaining), reader)), err
}

func normalizeText(text interface{}) string {
	switch v := text.(type) {
	case string:
		return v
	case []interface{}:
		var sb strings.Builder
		for _, item := range v {
			if s, ok := item.(string); ok {
				sb.WriteString(s)
			}
		}
		return sb.String()
	default:
		return ""
	}
}
