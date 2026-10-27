`responses-sse-redacted.txt` is the supplied redacted client capture from CPA
v8.0.44 with Codex OAuth. Tests split its reframed SSE into the delimiter-free
scanner lines the interceptor actually receives. All credential/cache/user
identifiers in the supplied capture were already redacted.

Terminal blank separator lines are omitted; tests supply line endings and frame
separators explicitly for each delivery mode. Payload lines are unchanged.
