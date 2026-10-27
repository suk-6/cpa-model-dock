package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

var initialModels = []string{"gpt-6.1-sol", "gpt-6.9-sol", "gpt-6.10-sol", "gpt-6.10-sol-mini", "gpt-6.11-sol-preview", "gpt-6-astra", "claude-opus-4-5-20251101", "other-model-x"}

type call struct {
	Path, Model string
	Stream      bool
}
type upstream struct {
	mu     sync.Mutex
	models []string
	calls  []call
	server *http.Server
	url    string
}

func startUpstream() (*upstream, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bind fake upstream on 127.0.0.1: %w", err)
	}
	u := &upstream{models: append([]string(nil), initialModels...), url: "http://" + ln.Addr().String()}
	u.server = &http.Server{Handler: http.HandlerFunc(u.serve), ReadHeaderTimeout: 5 * time.Second}
	go u.server.Serve(ln)
	return u, nil
}
func (u *upstream) snapshot() []call {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]call(nil), u.calls...)
}
func (u *upstream) setModels(models []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.models = append([]string(nil), models...)
}
func (u *upstream) modelList() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.models...)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (u *upstream) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && r.URL.Path == "/v1/models" {
		data := []map[string]any{}
		for _, m := range u.modelList() {
			data = append(data, map[string]any{"id": m, "object": "model", "created": 1700000000, "owned_by": "e2e"})
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": data})
		return
	}
	if r.Method != "POST" || (r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/v1/responses") {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	u.mu.Lock()
	u.calls = append(u.calls, call{r.URL.Path, body.Model, body.Stream})
	found := false
	for _, m := range u.models {
		if m == body.Model {
			found = true
		}
	}
	u.mu.Unlock()
	if !found {
		writeJSON(w, 404, map[string]any{"error": map[string]any{"message": "unknown fake model", "type": "invalid_request_error", "code": "model_not_found"}})
		return
	}
	if r.URL.Path == "/v1/responses" {
		u.responses(w, body.Model, body.Stream)
		return
	}
	base := map[string]any{"id": "chatcmpl-e2e", "object": "chat.completion", "created": 1700000000, "model": body.Model, "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "e2e-ok"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}
	if !body.Stream {
		writeJSON(w, 200, base)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	for _, choice := range []any{
		map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "e2e-ok"}, "finish_reason": nil},
		map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"},
	} {
		chunk := map[string]any{"id": "chatcmpl-e2e", "object": "chat.completion.chunk", "created": 1700000000, "model": body.Model, "choices": []any{choice}}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		w.(http.Flusher).Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	w.(http.Flusher).Flush()
}
func (u *upstream) responses(w http.ResponseWriter, model string, stream bool) {
	response := map[string]any{"id": "resp_e2e", "object": "response", "created_at": 1700000000, "status": "completed", "model": model, "output": []any{map[string]any{"type": "message", "id": "msg_e2e", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "e2e-ok", "annotations": []any{}}}}}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
	if !stream {
		writeJSON(w, 200, response)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for i, event := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": "resp_e2e", "object": "response", "status": "in_progress", "model": model}},
		{"type": "response.output_text.delta", "item_id": "msg_e2e", "output_index": 0, "content_index": 0, "delta": "e2e-ok"},
		{"type": "response.completed", "response": response},
	} {
		event["sequence_number"] = i
		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
		w.(http.Flusher).Flush()
	}
}
