package hooks

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const maxPendingBytes = 8 << 20

// Stream uses only pinned request state. Schema 6/7 payload chunks omit request
// bodies and history; neither is needed to identify the alias or parse fragments.
func (h *Hooks) Stream(req pluginapi.StreamChunkInterceptRequest) (out pluginapi.StreamChunkInterceptResponse) {
	defer func() {
		if recover() != nil {
			out = pluginapi.StreamChunkInterceptResponse{DropChunk: true, ClearHeaders: append([]string(nil), changedEntityHeaders...)}
		}
	}()
	name := requestedName(req.RequestedModel, req.Model)
	p := h.pin(req.RequestID, name, req.Model, false)
	p.mu.Lock()
	defer p.mu.Unlock()
	if !sameRequest(p, name) || !p.decision.Allowed {
		p.failed = true
	}
	if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex {
		if p.failed || p.rewrite {
			out.Headers = streamHeaders()
			out.ClearHeaders = append([]string(nil), changedEntityHeaders...)
		}
		return out
	}
	if p.failed {
		return failedChunk(p, req.SourceFormat)
	}
	if !p.rewrite {
		return out
	}
	if req.RequestID == "" {
		p.failed = true
		return failedChunk(p, req.SourceFormat)
	}
	if len(p.pending)+len(req.Body) > maxPendingBytes {
		p.pending = nil
		p.failed = true
		return failedChunk(p, req.SourceFormat)
	}
	p.pending = append(p.pending, req.Body...)
	body, rest, err := rewriteStream(p.pending, req.SourceFormat, p.decision.Alias.Name)
	if err != nil {
		p.pending = nil
		p.failed = true
		return failedChunk(p, req.SourceFormat)
	}
	p.pending = bytes.Clone(rest)
	if len(body) == 0 {
		out.DropChunk = true
	} else {
		out.Body = body
	}
	return out
}

func failedChunk(p *requestPin, format string) pluginapi.StreamChunkInterceptResponse {
	if p.errorSent {
		return pluginapi.StreamChunkInterceptResponse{DropChunk: true}
	}
	p.errorSent = true
	return pluginapi.StreamChunkInterceptResponse{Body: streamFailure(format), ClearHeaders: append([]string(nil), changedEntityHeaders...)}
}

func rewriteStream(input []byte, format, alias string) (output, rest []byte, err error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return bytes.Clone(input), nil, nil
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return bytes.Clone(input), nil, nil
	}
	if bytes.HasPrefix([]byte("[DONE]"), trimmed) {
		return nil, input, nil
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		// Chat and WebSocket deliveries are JSON events rather than SSE frames.
		// Decode complete JSON values while retaining a partial trailing value.
		remaining := input
		for len(bytes.TrimSpace(remaining)) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(remaining))
			var raw json.RawMessage
			if decodeErr := decoder.Decode(&raw); decodeErr != nil {
				if decodeErr == io.ErrUnexpectedEOF || decodeErr == io.EOF {
					return output, remaining, nil
				}
				return nil, nil, errPayload
			}
			encoded, rewriteErr := rewriteModel(raw, format, alias)
			if rewriteErr != nil {
				return nil, nil, rewriteErr
			}
			output = append(output, encoded...)
			remaining = remaining[decoder.InputOffset():]
		}
		output = append(output, remaining...)
		return output, nil, nil
	}
	for len(input) > 0 {
		end := sseFrameEnd(input)
		if end == 0 {
			return output, input, nil
		}
		frame, rewriteErr := rewriteSSEFrame(input[:end], format, alias)
		if rewriteErr != nil {
			return nil, nil, rewriteErr
		}
		output = append(output, frame...)
		input = input[end:]
	}
	return output, nil, nil
}

// sseFrameEnd honors LF, CRLF and CR line endings and never splits a CRLF
// across transport chunks. The blank line, including its delimiter, is retained.
func sseFrameEnd(data []byte) int {
	lineStart := 0
	for i := 0; i < len(data); i++ {
		if data[i] != '\n' && data[i] != '\r' {
			continue
		}
		end := i + 1
		if data[i] == '\r' {
			if end == len(data) {
				return 0
			}
			if data[end] == '\n' {
				end++
			}
		}
		if i == lineStart {
			return end
		}
		lineStart, i = end, end-1
	}
	return 0
}

func rewriteSSEFrame(frame []byte, format, alias string) ([]byte, error) {
	type line struct {
		body, ending []byte
		data         bool
		prefix       []byte
	}
	var lines []line
	var values [][]byte
	for offset := 0; offset < len(frame); {
		end := offset
		for end < len(frame) && frame[end] != '\n' && frame[end] != '\r' {
			end++
		}
		next := end
		if next < len(frame) {
			next++
			if frame[end] == '\r' && next < len(frame) && frame[next] == '\n' {
				next++
			}
		}
		part := line{body: frame[offset:end], ending: frame[end:next]}
		if bytes.Equal(part.body, []byte("data")) {
			part.data, part.prefix = true, []byte("data:")
			values = append(values, nil)
		} else if bytes.HasPrefix(part.body, []byte("data:")) {
			valueStart := len("data:")
			if len(part.body) > valueStart && part.body[valueStart] == ' ' {
				valueStart++
			}
			part.data, part.prefix = true, part.body[:valueStart]
			values = append(values, part.body[valueStart:])
		}
		lines = append(lines, part)
		offset = next
	}
	if len(values) == 0 {
		return frame, nil
	}
	data := bytes.Join(values, []byte("\n"))
	if len(bytes.TrimSpace(data)) == 0 || strings.TrimSpace(string(data)) == "[DONE]" {
		return frame, nil
	}
	encoded, err := rewriteModel(data, format, alias)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(encoded, data) {
		return frame, nil
	}
	var result []byte
	written := false
	for _, part := range lines {
		if part.data {
			result = append(result, part.prefix...)
			// JSON was parsed with SSE's multiline data semantics. Keep all
			// data lines; subsequent empty data lines add only legal JSON whitespace.
			if !written {
				result = append(result, encoded...)
				written = true
			}
		} else {
			result = append(result, part.body...)
		}
		result = append(result, part.ending...)
	}
	return result, nil
}
