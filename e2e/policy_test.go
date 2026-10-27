package main

import "testing"

func TestProtocolPolicyDenials(t *testing.T) {
	for _, tc := range []struct {
		path   string
		status int
		body   string
		want   bool
	}{
		{"/v1/chat/completions", 404, `{"error":{"code":"model_not_found"}}`, true},
		{"/v1/messages", 404, `{"type":"error","error":{"type":"not_found_error","message":"Model not found"}}`, true},
		{"/v1/messages", 403, `{"type":"error","error":{"type":"permission_error","message":"Model is not allowed"}}`, true},
		{"/gemini", 404, `{"error":{"code":404,"status":"NOT_FOUND","message":"Model not found"}}`, true},
		{"/gemini", 403, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Model is not allowed"}}`, true},
		{"/v1/messages", 404, `{"error":"unsupported protocol"}`, false},
		{"/gemini", 404, `{"error":{"code":404,"message":"unknown model"}}`, false},
		{"/gemini", 404, `not json`, false},
	} {
		if got := protocolPolicyDenial(tc.path, response{status: tc.status, body: []byte(tc.body)}); got != tc.want {
			t.Errorf("%s %s: got %v", tc.path, tc.body, got)
		}
	}
}
