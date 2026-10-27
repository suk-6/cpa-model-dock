package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func collectStream(t *testing.T, format string, chunks [][]byte) []byte {
	t.Helper()
	h := hooksFor(t, fixture("gpt-6.1-sol", true))
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "stream", RequestedModel: "friendly", Model: "gpt-6.1-sol", Stream: true}), 0)
	init := h.Stream(pluginapi.StreamChunkInterceptRequest{RequestID: "stream", RequestedModel: "friendly", Model: "gpt-6.1-sol", SourceFormat: format, ChunkIndex: pluginapi.StreamChunkHeaderInitIndex})
	if !containsFold(init.ClearHeaders, "Content-Length") {
		t.Fatal("stream header initialization retained entity size")
	}
	var delivered []byte
	for i, chunk := range chunks {
		original := bytes.Clone(chunk)
		// Schema 7 deliberately omits OriginalRequest, RequestBody and HistoryChunks.
		out := h.Stream(pluginapi.StreamChunkInterceptRequest{RequestID: "stream", RequestedModel: "friendly", Model: "gpt-6.1-sol", SourceFormat: format, ChunkIndex: i, Body: chunk})
		if !bytes.Equal(chunk, original) {
			t.Fatal("read-only stream chunk mutated")
		}
		if out.DropChunk {
			continue
		}
		if len(out.Body) > 0 {
			delivered = append(delivered, out.Body...)
		} else {
			delivered = append(delivered, chunk...)
		}
	}
	return delivered
}

func TestFragmentedJSONStreamAtEveryBoundary(t *testing.T) {
	input := []byte(`{"model":"gpt-6.1-sol","choices":[{"delta":{"content":"gpt-6.1-sol"}}],"number":9007199254740993}`)
	for split := 1; split < len(input); split++ {
		body := collectStream(t, "openai", [][]byte{input[:split], input[split:]})
		if !json.Valid(body) || !bytes.Contains(body, []byte(`"model":"friendly"`)) || !bytes.Contains(body, []byte(`"content":"gpt-6.1-sol"`)) || !bytes.Contains(body, []byte(`9007199254740993`)) {
			t.Fatalf("split %d: %s", split, body)
		}
	}
}

func TestFragmentedSSEAtEveryBoundary(t *testing.T) {
	input := []byte(": keepalive\r\n\r\nid: event-1\r\nevent: response.created\r\ndata: {\"response\":{\"model\":\"gpt-6.1-sol\",\"instructions\":\"gpt-6.1-sol\"}}\r\n\r\ndata: [DONE]\r\n\r\n")
	for split := 1; split < len(input); split++ {
		out := collectStream(t, "openai-response", [][]byte{input[:split], input[split:]})
		if !bytes.Contains(out, []byte(`"model":"friendly"`)) || !bytes.Contains(out, []byte(`"instructions":"gpt-6.1-sol"`)) {
			t.Fatalf("split %d: %s", split, out)
		}
		for _, framing := range []string{": keepalive\r\n\r\n", "id: event-1\r\n", "event: response.created\r\n", "data: [DONE]\r\n\r\n"} {
			if !bytes.Contains(out, []byte(framing)) {
				t.Fatalf("split %d lost framing %q: %q", split, framing, out)
			}
		}
		if bytes.Contains(bytes.ReplaceAll(out, []byte("\r\n"), nil), []byte("\n")) {
			t.Fatalf("split %d changed CRLF: %q", split, out)
		}
	}
}

func TestSSEMultilineDataMultipleEventsAndContent(t *testing.T) {
	input := []byte("event: message_start\nid: m-1\nretry: 1000\ndata: {\"type\":\"message_start\",\ndata: \"message\":{\"model\":\"gpt-6.1-sol\",\"content\":[]}}\n\nevent: content_block_delta\ndata: {\"delta\":{\"text\":\"gpt-6.1-sol\"}}\n\n: ping\n\n")
	chunks := make([][]byte, len(input))
	for i := range input {
		chunks[i] = input[i : i+1]
	}
	out := collectStream(t, "claude", chunks)
	if !bytes.Contains(out, []byte(`"model":"friendly"`)) || !bytes.Contains(out, []byte(`"text":"gpt-6.1-sol"`)) {
		t.Fatal(string(out))
	}
	for _, keep := range []string{"event: message_start\n", "id: m-1\n", "retry: 1000\n", "event: content_block_delta\n", ": ping\n\n"} {
		if !bytes.Contains(out, []byte(keep)) {
			t.Fatalf("lost framing %q: %s", keep, out)
		}
	}
	// Verify that the multiline data still forms a valid JSON event after joining.
	frame := bytes.Split(out, []byte("\n\n"))[0]
	var values [][]byte
	for _, line := range bytes.Split(frame, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data:")) {
			values = append(values, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" ")))
		}
	}
	if !json.Valid(bytes.Join(values, []byte("\n"))) {
		t.Fatalf("multiline SSE JSON invalid: %s", out)
	}
}

func TestGeminiStreamAndRawJSONEvents(t *testing.T) {
	out := collectStream(t, "gemini", [][]byte{[]byte(`[{"modelVer`), []byte(`sion":"gpt-6.1-sol","candidates":[]}]`)})
	if !json.Valid(out) || !bytes.Contains(out, []byte(`"modelVersion":"friendly"`)) {
		t.Fatal(string(out))
	}
	out = collectStream(t, "openai-response", [][]byte{[]byte(`{"response":{"model":"gpt-6.1-sol"}}`), []byte(`{"response":{"model":"gpt-6.1-sol"}}`)})
	decoder := json.NewDecoder(bytes.NewReader(out))
	for i := 0; i < 2; i++ {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event["response"].(map[string]any)["model"] != "friendly" {
			t.Fatal(event)
		}
	}
}

func TestStreamRetainsPartialTrailingEvent(t *testing.T) {
	out := collectStream(t, "openai-response", [][]byte{[]byte("data: {\"response\":{\"model\":\"gpt-6.1-sol\"}}\n\ndata: {\"response\":"), []byte("{\"model\":\"gpt-6.1-sol\"}}\n\n")})
	if bytes.Count(out, []byte(`"model":"friendly"`)) != 2 {
		t.Fatal(string(out))
	}
}

func TestStreamMalformedPayloadFailsClosed(t *testing.T) {
	h := hooksFor(t, fixture("gpt-6.1-sol", true))
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "bad", RequestedModel: "friendly", Model: "gpt-6.1-sol"}), 0)
	first := h.Stream(pluginapi.StreamChunkInterceptRequest{RequestID: "bad", RequestedModel: "friendly", Model: "gpt-6.1-sol", SourceFormat: "claude", Body: []byte("data: {not json secret}\n\n")})
	if first.DropChunk || !bytes.HasPrefix(first.Body, []byte("event: error\n")) || bytes.Contains(first.Body, []byte("secret")) {
		t.Fatalf("malformed event failed open: %+v", first)
	}
	second := h.Stream(pluginapi.StreamChunkInterceptRequest{RequestID: "bad", RequestedModel: "friendly", Model: "gpt-6.1-sol", SourceFormat: "claude", Body: []byte("data: {\"secret\":true}\n\n"), ChunkIndex: 1})
	if !second.DropChunk {
		t.Fatal("stream resumed after parser failure")
	}
}

func TestStreamPendingBoundAndLifecycleRelease(t *testing.T) {
	h := hooksFor(t, fixture("gpt-6.1-sol", true))
	h.Before(pluginapi.RequestInterceptRequest{RequestID: "large", RequestedModel: "friendly", Model: "gpt-6.1-sol"})
	tooLarge := []byte("data: " + strings.Repeat("x", maxPendingBytes))
	out := h.Stream(pluginapi.StreamChunkInterceptRequest{RequestID: "large", RequestedModel: "friendly", Model: "gpt-6.1-sol", SourceFormat: "openai-response", Body: tooLarge})
	if bytes.Contains(out.Body, []byte(strings.Repeat("x", 64))) || len(out.Body) == 0 {
		t.Fatal("unbounded frame failed open")
	}
	h.Complete(pluginapi.RequestCompletion{RequestID: "large", Outcome: pluginapi.RequestCompletionCanceled})
	h.mu.Lock()
	_, exists := h.pins["large"]
	h.mu.Unlock()
	if exists {
		t.Fatal("canceled stream retained buffer")
	}
}

func TestStreamWithoutRewritePreservesOpaquePayload(t *testing.T) {
	h := hooksFor(t, fixture("gpt-6.1-sol", false))
	h.Before(pluginapi.RequestInterceptRequest{RequestID: "opaque", RequestedModel: "friendly", Model: "gpt-6.1-sol"})
	out := h.Stream(pluginapi.StreamChunkInterceptRequest{RequestID: "opaque", RequestedModel: "friendly", Model: "gpt-6.1-sol", Body: []byte("opaque bytes")})
	if out.DropChunk || len(out.Body) != 0 {
		t.Fatalf("rewrite opt-out was ignored: %+v", out)
	}
}

func TestStreamDeniedAndMissingRequestIdentity(t *testing.T) {
	h := hooksFor(t, fixture("gpt-6.1-sol", true))
	out := h.Stream(pluginapi.StreamChunkInterceptRequest{RequestID: "denied", RequestedModel: "other", Model: "other", SourceFormat: "openai", Body: []byte(`{"secret":true}`)})
	if bytes.Contains(out.Body, []byte("secret")) || len(out.Body) == 0 {
		t.Fatal("denied stream leaked a chunk")
	}
	out = h.Stream(pluginapi.StreamChunkInterceptRequest{RequestedModel: "friendly", Model: "gpt-6.1-sol", SourceFormat: "openai", Body: []byte(`{"model":"gpt-6.1-sol"}`)})
	if bytes.Contains(out.Body, []byte("gpt-6.1-sol")) || len(out.Body) == 0 {
		t.Fatal("fragment parser accepted uncorrelated chunks")
	}
}

// minimalResponsesFramer copies the frame-detection rules from CLIProxyAPI
// v8.0.44 sdk/api/handlers/openai/openai_responses_handlers.go (MIT), omitting
// output repair, telemetry filtering and completion synthesis. Keep chunk
// boundaries here: merely joining outputs would conceal the Codex regression.
type minimalResponsesFramer struct {
	pending []byte
	frames  [][]byte
}

func (f *minimalResponsesFramer) write(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	has := func(b []byte, prefix string) bool {
		for _, line := range bytes.Split(b, []byte("\n")) {
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte(prefix)) {
				return true
			}
		}
		return false
	}
	valid := func(b []byte) bool {
		var values [][]byte
		for _, line := range bytes.Split(b, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if bytes.HasPrefix(line, []byte("data:")) {
				values = append(values, bytes.TrimSpace(line[5:]))
			}
		}
		data := bytes.Join(values, []byte("\n"))
		return len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) || json.Valid(data)
	}
	emit := func() { f.frames = append(f.frames, bytes.Clone(f.pending)); f.pending = nil }
	if len(bytes.TrimSpace(f.pending)) > 0 && !has(f.pending, "event:") && has(f.pending, "data:") && valid(f.pending) && bytes.HasPrefix(bytes.TrimLeft(chunk, " \t\r\n"), []byte("data:")) {
		emit()
	}
	newField := false
	for _, prefix := range []string{"data:", "event:", "id:", "retry:", ":"} {
		if bytes.HasPrefix(bytes.TrimLeft(chunk, " \t"), []byte(prefix)) {
			newField = true
			break
		}
	}
	if len(f.pending) > 0 && !bytes.ContainsAny(f.pending[len(f.pending)-1:], "\r\n") && !bytes.ContainsAny(chunk[:1], "\r\n") && newField {
		f.pending = append(f.pending, '\n')
	}
	f.pending = append(f.pending, chunk...)
	for {
		n := 0
		for _, sep := range []string{"\n\n", "\r\n\r\n"} {
			if i := bytes.Index(f.pending, []byte(sep)); i >= 0 && (n == 0 || i+len(sep) < n) {
				n = i + len(sep)
			}
		}
		if n == 0 {
			break
		}
		f.frames = append(f.frames, bytes.Clone(f.pending[:n]))
		f.pending = bytes.Clone(f.pending[n:])
	}
	if len(bytes.TrimSpace(f.pending)) == 0 {
		f.pending = nil
		return
	}
	if has(f.pending, "event:") && has(f.pending, "data:") && valid(f.pending) {
		emit()
	}
}

func capturedResponsesLines(t *testing.T, ending string) [][]byte {
	t.Helper()
	capture, err := os.ReadFile("testdata/responses-sse-redacted.txt")
	if err != nil {
		t.Fatal(err)
	}
	var chunks [][]byte
	for _, line := range bytes.Split(capture, []byte("\n")) {
		if len(line) > 0 {
			chunks = append(chunks, append(bytes.Clone(line), ending...))
		}
	}
	return chunks
}

func TestCodexResponsesLinesRewriteImmediatelyAndReachFramer(t *testing.T) {
	for _, ending := range []string{"", "\n", "\r\n"} {
		t.Run(fmt.Sprintf("ending_%q", ending), func(t *testing.T) {
			h := hooksFor(t, fixture("gpt-6.1-sol", true))
			h.Before(pluginapi.RequestInterceptRequest{RequestID: "codex", RequestedModel: "friendly", Model: "gpt-6.1-sol", Stream: true})
			var framer minimalResponsesFramer
			for i, chunk := range capturedResponsesLines(t, ending) {
				out := h.Stream(pluginapi.StreamChunkInterceptRequest{RequestID: "codex", RequestedModel: "friendly", Model: "gpt-6.1-sol", SourceFormat: "openai-response", ChunkIndex: i, Body: chunk})
				if out.DropChunk || len(out.Body) == 0 {
					t.Fatalf("complete line %d withheld: %q", i, chunk)
				}
				if bytes.HasPrefix(chunk, []byte("data:")) {
					var original, event map[string]json.RawMessage
					if err := json.Unmarshal(bytes.TrimSpace(chunk[5:]), &original); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(bytes.TrimSpace(out.Body[5:]), &event); err != nil {
						t.Fatal(err)
					}
					if raw, ok := original["response"]; ok {
						var response map[string]json.RawMessage
						json.Unmarshal(raw, &response)
						response["model"] = json.RawMessage(`"friendly"`)
						expected, _ := json.Marshal(response)
						original["response"] = expected
					}
					want, _ := json.Marshal(original)
					got, _ := json.Marshal(event)
					if !bytes.Equal(want, got) {
						t.Fatalf("line %d changed non-model fields", i)
					}
					if !bytes.HasSuffix(out.Body, []byte(ending)) || (ending == "" && bytes.HasSuffix(out.Body, []byte("\n"))) {
						t.Fatalf("line endings changed: %q", out.Body)
					}
				} else if !bytes.Equal(out.Body, chunk) {
					t.Fatalf("metadata line changed: %q -> %q", chunk, out.Body)
				}
				framer.write(out.Body)
			}
			models := 0
			for _, frame := range framer.frames {
				for _, line := range bytes.Split(frame, []byte("\n")) {
					if !bytes.HasPrefix(line, []byte("data:")) {
						continue
					}
					var event struct {
						Response *struct {
							Model string `json:"model"`
						} `json:"response"`
					}
					if json.Unmarshal(bytes.TrimSpace(line[5:]), &event) != nil {
						t.Fatalf("invalid frame %q", frame)
					}
					if event.Response != nil {
						if event.Response.Model != "friendly" {
							t.Fatalf("model=%q", event.Response.Model)
						}
						models++
					}
				}
			}
			if len(framer.frames) == 0 || models != 3 {
				t.Fatalf("data frames=%d response models=%d want 3", len(framer.frames), models)
			}
		})
	}
}

func TestSSEMixedGranularities(t *testing.T) {
	lines := capturedResponsesLines(t, "\n")
	// Coalesce complete frames, fragment the next value at every byte, then
	// resume complete lines and finish with a complete event in one chunk.
	var input []byte
	for _, line := range lines {
		input = append(input, line...)
	}
	for split := 1; split < len(input); split++ {
		chunks := [][]byte{input[:split], input[split:]}
		out := collectStream(t, "openai-response", chunks)
		if bytes.Count(out, []byte(`"model":"friendly"`)) != 3 || bytes.Contains(out, []byte(`"model":"gpt-6.1-sol"`)) {
			t.Fatalf("split %d: %s", split, out)
		}
	}
	var chunks [][]byte
	chunks = append(chunks, bytes.Join(lines[:4], nil))
	for _, b := range bytes.Join(lines[4:8], nil) {
		chunks = append(chunks, []byte{b})
	}
	chunks = append(chunks, lines[8:]...)
	out := collectStream(t, "openai-response", chunks)
	if bytes.Count(out, []byte(`"model":"friendly"`)) != 3 {
		t.Fatal(string(out))
	}
}

func TestSSEOpaqueDataAndMetadataPassThrough(t *testing.T) {
	for _, value := range []string{"notification", "42", "true", "null", `"gpt-6.1-sol"`, "[DONE]"} {
		input := []byte("event: notice\r\nid: 1\r\n: ping\r\ndata: " + value + "\r\n\r\n")
		out := collectStream(t, "openai-response", [][]byte{input})
		if !bytes.Equal(out, input) {
			t.Fatalf("opaque data changed %q -> %q", input, out)
		}
	}
	input := []byte("id: {\"model\":\"gpt-6.1-sol\"}\nevent: response.created\ndata: {\"response\":{\"model\":\"gpt-6.1-sol\"}}\n\n")
	var chunks [][]byte
	for i := range input {
		chunks = append(chunks, input[i:i+1])
	}
	out := collectStream(t, "openai-response", chunks)
	if !bytes.Contains(out, []byte("id: {\"model\":\"gpt-6.1-sol\"}\n")) || !bytes.Contains(out, []byte(`"model":"friendly"`)) {
		t.Fatal(string(out))
	}
}

func TestMalformedDataWithoutDelimiterFailsClosed(t *testing.T) {
	for _, input := range []string{"data: {not JSON secret}", "data: {\"model\":\"secret\"}junk\n", "data: {\"model\":\n\n"} {
		out := collectStream(t, "openai-response", [][]byte{[]byte(input)})
		if bytes.Contains(out, []byte("secret")) || !bytes.Contains(out, []byte("policy_unavailable")) {
			t.Fatalf("failed open: %q", out)
		}
	}
}

func TestDelimiterFreeMetadataIsImmediate(t *testing.T) {
	h := hooksFor(t, fixture("gpt-6.1-sol", true))
	h.Before(pluginapi.RequestInterceptRequest{RequestID: "meta", RequestedModel: "friendly", Model: "gpt-6.1-sol"})
	for i, line := range []string{"event: response.created", "id: evt-1", ": keepalive", "retry: 1000", "custom: opaque"} {
		out := h.Stream(pluginapi.StreamChunkInterceptRequest{RequestID: "meta", RequestedModel: "friendly", Model: "gpt-6.1-sol", SourceFormat: "openai-response", ChunkIndex: i, Body: []byte(line)})
		if out.DropChunk || string(out.Body) != line {
			t.Fatalf("metadata delayed or changed: %q %+v", line, out)
		}
	}
}
