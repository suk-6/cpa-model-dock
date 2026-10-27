package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

const base = "/v0/management/plugins/cpa-model-dock"

type row struct {
	Status string `json:"status"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}
type response struct {
	status int
	body   []byte
	header http.Header
}
type skipped string
type harness struct {
	ctx                                             context.Context
	dir, bin, url, config, clientKey, managementKey string
	upstream                                        *upstream
	client                                          *http.Client
	cmd                                             *exec.Cmd
	done                                            chan error
	log                                             *os.File
	rows                                            []row
	noPlugin                                        bool
	routable                                        map[string]bool
	guardBaseline                                   map[string]response
}

func main() { os.Exit(run()) }
func run() (exitCode int) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	h := &harness{ctx: ctx, dir: os.Getenv("E2E_WORK_DIR"), bin: os.Getenv("CPA_BIN"), noPlugin: os.Getenv("E2E_NO_PLUGIN") == "1", routable: map[string]bool{}, guardBaseline: map[string]response{}}
	h.client = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer func() {
		h.stop()
		if h.upstream != nil {
			_ = h.upstream.server.Close()
		}
		if ctx.Err() != nil {
			h.rows = append(h.rows, row{"FAIL", "interrupted", ctx.Err().Error()})
		}
		h.report()
		for _, r := range h.rows {
			if r.Status == "FAIL" {
				exitCode = 1
			}
		}
	}()
	if !h.check("start CPA + fake upstream", func() string {
		h.setup()
		return "isolated loopback listeners; embedded CPA catalogs; management authenticated"
	}) {
		return 1
	}
	h.baseline()
	if h.noPlugin {
		h.check("configured catalog removal + reload", func() string {
			h.removeLatest()
			h.waitConfigAbsent("gpt-6.10-sol")
			h.eventually("removed model no longer advertised", func() bool {
				return !contains(h.models(), "gpt-6.10-sol") && !contains(h.models(), "prefix/gpt-6.10-sol")
			})
			h.upstream.setModels(initialModels)
			h.writeConfig()
			h.waitListed("gpt-6.10-sol")
			return "fake /models and CPA configured model list remove/restore together"
		})
		h.check("no-plugin restart + recorder", func() string {
			h.stop()
			h.start()
			h.waitListed("gpt-6.1-sol")
			h.chat("cpa-native-sol", "gpt-6.1-sol", false, "gpt-6.1-sol")
			h.chat("gpt-6.1-sol", "gpt-6.1-sol", true, "gpt-6.1-sol")
			return "same config/key and upstream survive CPA restart; JSON/SSE recorder verified"
		})

		for _, name := range pluginScenarios {
			h.rows = append(h.rows, row{"SKIP", name, "E2E_NO_PLUGIN=1"})
		}
		return 0
	}
	h.pluginChecks()
	h.alphaSafetyChecks()
	h.uiChecks()
	return 0
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func require(ok bool, format string, args ...any) {
	if !ok {
		panic(fmt.Errorf(format, args...))
	}
}
func token() string {
	b := make([]byte, 24)
	must(func() error { _, err := rand.Read(b); return err }())
	return hex.EncodeToString(b)
}
func (h *harness) check(name string, f func() string) (ok bool) {
	fmt.Printf("RUN  %s\n", name)
	started := time.Now()
	defer func() {
		if p := recover(); p != nil {
			status := "FAIL"
			if _, yes := p.(skipped); yes {
				status = "SKIP"
			}
			detail := fmt.Sprint(p)
			h.rows = append(h.rows, row{status, name, detail})
			fmt.Printf("%s %s: %s\n", status, name, detail)
		}
	}()
	detail := f()
	h.rows = append(h.rows, row{"PASS", name, detail})
	fmt.Printf("PASS %s (%s): %s\n", name, time.Since(started).Round(time.Millisecond), detail)
	return true
}
func (h *harness) report() {
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "RESULT\tSCENARIO\tDETAIL")
	counts := map[string]int{}
	for _, r := range h.rows {
		counts[r.Status]++
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.Status, r.Name, strings.ReplaceAll(strings.ReplaceAll(r.Detail, "\n", " "), "\t", " "))
	}
	w.Flush()
	fmt.Printf("\nPASS=%d FAIL=%d SKIP=%d\n", counts["PASS"], counts["FAIL"], counts["SKIP"])
	data, err := json.MarshalIndent(h.rows, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(h.dir, "results.json"), append(data, '\n'), 0600)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
	}
}
func (h *harness) setup() {
	require(h.dir != "" && h.bin != "", "use e2e/run.sh (CPA_BIN/E2E_WORK_DIR missing)")
	var err error
	h.upstream, err = startUpstream()
	must(err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	h.url = "http://" + ln.Addr().String()
	must(ln.Close())
	h.clientKey = token()
	h.managementKey = token()
	h.config = filepath.Join(h.dir, "config.yaml")
	for _, d := range []string{"auth", "state", "plugins"} {
		must(os.MkdirAll(filepath.Join(h.dir, d), 0700))
	}
	if !h.noPlugin {
		ext := map[string]string{"darwin": ".dylib", "linux": ".so", "windows": ".dll"}[runtime.GOOS]
		require(ext != "", "unsupported native plugin platform: %s", runtime.GOOS)
		// CPA requires regular directory entries, so copy rather than symlink.
		data, err := os.ReadFile(os.Getenv("CPA_MODEL_DOCK_LIB"))
		must(err)
		must(os.WriteFile(filepath.Join(h.dir, "plugins", "cpa-model-dock"+ext), data, 0700))
	}
	h.writeConfig()
	h.start()
	h.waitListed("gpt-6.1-sol")
}
func (h *harness) writeConfig() {
	providers := []any{}
	for _, prefix := range []string{"", "prefix"} {
		models := []any{}
		for _, m := range h.upstream.modelList() {
			models = append(models, map[string]any{"name": m, "alias": m})
		}
		models = append(models, map[string]any{"name": "gpt-6.1-sol", "alias": "cpa-native-sol"})
		name := "e2e-openai"
		if prefix != "" {
			name += "-prefixed"
		}
		providers = append(providers, map[string]any{"name": name, "prefix": prefix, "base-url": h.upstream.url + "/v1", "request-retry": 0, "keys": []any{map[string]any{"api-key": "e2e-upstream-key", "proxy-url": "direct"}}, "models": models})
	}
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(h.url, "http://"))
	var portNum int
	_, err := fmt.Sscan(port, &portNum)
	must(err)
	cfg := map[string]any{
		"config-version": 8, "server": map[string]any{"host": "127.0.0.1", "port": portNum},
		"access":     map[string]any{"api-keys": []string{h.clientKey}},
		"management": map[string]any{"secret-key": h.managementKey, "allow-remote": false, "disable-control-panel": true, "disable-auto-update-panel": true},
		"oauth":      map[string]any{"auth-dir": filepath.Join(h.dir, "auth")},
		"routing":    map[string]any{"force-model-prefix": false, "retry": map[string]any{"request-retry": 0, "max-retry-interval": 0}},
		"api-keys":   map[string]any{"openai-compatibility": providers},
		"plugins":    map[string]any{"enabled": !h.noPlugin, "dir": filepath.Join(h.dir, "plugins"), "configs": map[string]any{"cpa-model-dock": map[string]any{"enabled": true, "priority": 1000, "state-dir": filepath.Join(h.dir, "state"), "catalog-refresh-interval": "5m", "guard-uncovered-endpoints": true}}},
	}
	// JSON is a YAML subset; encode strings/paths without hand-built YAML quoting.
	data, err := json.MarshalIndent(cfg, "", "  ")
	must(err)
	temp := h.config + ".tmp"
	must(os.WriteFile(temp, append(data, '\n'), 0600))
	must(os.Rename(temp, h.config))
}
func (h *harness) start() {
	var err error
	h.log, err = os.OpenFile(filepath.Join(h.dir, "cpa.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	must(err)
	h.cmd = exec.Command(h.bin, "-config", h.config, "-local-model")
	h.cmd.Dir = h.dir
	// Keep developer database/Home credentials and proxy configuration out of the fixture.
	h.cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.dir, "TMPDIR=" + h.dir, "LANG=C", "NO_PROXY=*", "no_proxy=*"}
	h.cmd.Stdout = h.log
	h.cmd.Stderr = h.log
	must(h.cmd.Start())
	h.done = make(chan error, 1)
	cmd, done := h.cmd, h.done
	go func() { done <- cmd.Wait(); close(done) }()
	h.eventually("CPA management readiness", func() bool {
		select {
		case err := <-h.done:
			panic(fmt.Errorf("CPA exited before readiness: %v (see cpa.log)", err))
		default:
		}
		r, err := h.request("GET", "/v8/management/plugins", nil, h.managementKey, nil)
		return err == nil && r.status == 200
	})
}
func (h *harness) stop() {
	if h.cmd == nil || h.cmd.Process == nil {
		return
	}
	_ = h.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		_ = h.cmd.Process.Kill()
		<-h.done
	}
	if h.log != nil {
		_ = h.log.Close()
	}
	h.cmd = nil
}
func (h *harness) eventually(label string, f func() bool) {
	timeout := time.NewTimer(25 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		if f() {
			return
		}
		select {
		case <-h.ctx.Done():
			panic(h.ctx.Err())
		case <-timeout.C:
			panic(fmt.Errorf("timed out waiting for %s", label))
		case <-ticker.C:
		}
	}
}
func (h *harness) request(method, path string, body any, key string, headers map[string]string) (response, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return response{}, err
		}
	}
	req, err := http.NewRequestWithContext(h.ctx, method, h.url+path, bytes.NewReader(data))
	if err != nil {
		return response{}, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := h.client.Do(req)
	if err != nil {
		return response{}, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return response{res.StatusCode, b, res.Header}, err
}
func (h *harness) http(method, path string, body any, key string) response {
	r, err := h.request(method, path, body, key, nil)
	must(err)
	return r
}
func wantStatus(r response, status int) {
	require(r.status == status, "HTTP %d, want %d: %s", r.status, status, short(r.body))
}
func short(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}
func decode[T any](r response) T { var v T; must(json.Unmarshal(r.body, &v)); return v }
func (h *harness) mgmt(method, path string, body any) response {
	return h.http(method, base+path, body, h.managementKey)
}
func (h *harness) models() []string {
	r := h.http("GET", "/v1/models", nil, h.clientKey)
	wantStatus(r, 200)
	var v struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	must(json.Unmarshal(r.body, &v))
	ids := []string{}
	for _, m := range v.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if s == v {
			return true
		}
	}
	return false
}
func (h *harness) waitListed(model string) {
	h.eventually("model "+model, func() bool { return contains(h.models(), model) })
}

// Unlike v8 /config (an on-disk view), this endpoint reads the runtime config.
func (h *harness) waitConfigAbsent(model string)  { h.waitConfigured(model, false) }
func (h *harness) waitConfigPresent(model string) { h.waitConfigured(model, true) }
func (h *harness) waitConfigured(model string, present bool) {
	h.eventually(fmt.Sprintf("runtime CPA providers model=%s present=%v", model, present), func() bool {
		r := h.http("GET", "/v0/management/openai-compatibility", nil, h.managementKey)
		if r.status != 200 {
			return false
		}
		var cfg struct {
			Providers []struct {
				Models []struct {
					Name string `json:"name"`
				} `json:"models"`
			} `json:"openai-compatibility"`
		}
		if json.Unmarshal(r.body, &cfg) != nil || len(cfg.Providers) != 2 {
			return false
		}
		count := 0
		for _, p := range cfg.Providers {
			for _, m := range p.Models {
				if m.Name == model {
					count++
					break
				}
			}
		}
		if present {
			return count == 2
		}
		return count == 0
	})
}
func (h *harness) removeLatest() {
	models := []string{}
	for _, m := range initialModels {
		if m != "gpt-6.10-sol" {
			models = append(models, m)
		}
	}
	h.upstream.setModels(models)
	h.writeConfig()
}
func (h *harness) infer(path, model string, stream bool) response {
	var body any
	switch path {
	case "/v1/messages":
		body = map[string]any{"model": model, "max_tokens": 16, "messages": []any{map[string]any{"role": "user", "content": "hello"}}, "stream": stream}
	case "/v1/responses":
		body = map[string]any{"model": model, "input": "hello", "stream": stream}
	case "/gemini":
		path = "/v1beta/models/" + model + ":generateContent"
		body = map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello"}}}}}
	default:
		body = map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": "hello"}}, "stream": stream}
	}
	r, err := h.request("POST", path, body, h.clientKey, map[string]string{"anthropic-version": "2023-06-01"})
	must(err)
	return r
}
func (h *harness) assertCall(before int, model string, stream bool) {
	calls := h.upstream.snapshot()
	require(len(calls) == before+1, "upstream calls=%d, want %d: %+v", len(calls), before+1, calls)
	got := calls[before]
	require(got.Model == model && got.Stream == stream, "upstream received %+v; want model=%s stream=%v", got, model, stream)
}
func (h *harness) chat(model, target string, stream bool, responseModel string) {
	before := len(h.upstream.snapshot())
	r := h.infer("/v1/chat/completions", model, stream)
	wantStatus(r, 200)
	h.assertCall(before, target, stream)
	require(bytes.Contains(r.body, []byte("e2e-ok")), "missing fake output: %s", short(r.body))
	if !stream {
		v := decode[map[string]any](r)
		require(v["model"] == responseModel, "response model=%v want %s", v["model"], responseModel)
		return
	}
	require(strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream"), "stream Content-Type=%s", r.header.Get("Content-Type"))
	require(bytes.Contains(r.body, []byte("data: [DONE]")), "SSE did not complete: %s", short(r.body))
	chunks := 0
	for _, line := range strings.Split(string(r.body), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			continue
		}
		var v map[string]any
		must(json.Unmarshal([]byte(data), &v))
		require(v["model"] == responseModel, "SSE model=%v want %s", v["model"], responseModel)
		chunks++
	}
	require(chunks >= 2, "SSE chunks=%d want >=2", chunks)
}
func (h *harness) baseline() {
	h.check("client and management auth isolation", func() string {
		wantStatus(h.http("GET", "/v1/models", nil, ""), 401)
		wantStatus(h.http("GET", "/v8/management/plugins", nil, h.clientKey), 401)
		wantStatus(h.http("GET", "/v8/management/plugins", nil, h.managementKey), 200)
		return "missing client key and client key on management rejected"
	})
	h.check("fake upstream model catalog", func() string {
		req, err := http.NewRequestWithContext(h.ctx, "GET", h.upstream.url+"/v1/models", nil)
		must(err)
		res, err := h.client.Do(req)
		must(err)
		defer res.Body.Close()
		var v struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		must(json.NewDecoder(res.Body).Decode(&v))
		require(len(v.Data) == len(initialModels), "fake catalog size=%d", len(v.Data))
		ids := h.models()
		for _, m := range initialModels {
			require(contains(ids, m), "CPA missing configured model %s: %v", m, ids)
		}
		require(contains(ids, "prefix/gpt-6.1-sol") && contains(ids, "cpa-native-sol"), "prefix/native alias not published: %v", ids)
		return "all 8 fake models, credential prefix and CPA model alias advertised"
	})
	for _, m := range []string{"gpt-6.1-sol", "prefix/gpt-6.1-sol", "cpa-native-sol", "gpt-6.1-sol(high)", "other-model-x"} {
		model := m
		h.check("baseline route "+model, func() string {
			target := "gpt-6.1-sol"
			if model == "other-model-x" {
				target = model
			}
			h.chat(model, target, false, target)
			h.routable[model] = true
			return "recorder confirms upstream model=" + target
		})
	}
	h.check("baseline SSE chat", func() string {
		h.chat("gpt-6.1-sol", "gpt-6.1-sol", true, "gpt-6.1-sol")
		return "two JSON chunks, model fields, fake text and [DONE] verified"
	})
	h.check("baseline Responses translation", func() string {
		before := len(h.upstream.snapshot())
		r := h.infer("/v1/responses", "gpt-6.1-sol", false)
		wantStatus(r, 200)
		h.assertCall(before, "gpt-6.1-sol", false)
		require(bytes.Contains(r.body, []byte("e2e-ok")), "Responses missing text: %s", short(r.body))
		return "CPA /v1/responses reaches fake " + h.upstream.snapshot()[before].Path
	})
	for _, p := range []string{"/v1/messages", "/gemini"} {
		path := p
		h.check("baseline protocol "+path, func() string {
			before := len(h.upstream.snapshot())
			r := h.infer(path, "gpt-6.1-sol", false)
			if r.status != 200 {
				require(len(h.upstream.snapshot()) == before, "protocol failed after upstream call: HTTP %d %s", r.status, short(r.body))
				panic(skipped(fmt.Sprintf("CPA cannot route protocol: HTTP %d %s", r.status, short(r.body))))
			}
			h.assertCall(before, "gpt-6.1-sol", false)
			h.routable[path] = true
			if path == "/v1/messages" {
				before = len(h.upstream.snapshot())
				r = h.infer(path, "claude-fable-5-dd-los-1.6-tpg", false)
				wantStatus(r, 200)
				h.assertCall(before, "gpt-6.1-sol", false)
				h.routable[path+" cloak"] = true
			}
			return "translated protocol reaches OpenAI-compatible upstream"
		})
	}
	h.check("Alpha default pass-through", func() string {
		r := h.http("POST", "/v1/alpha/search", map[string]any{"model": "cpa-native-sol", "query": "fixture"}, h.clientKey)
		wantStatus(r, 503)
		require(!bytes.Contains(r.body, []byte("unsupported Codex Alpha Search model route target")) && bytes.Contains(r.body, []byte("auth_not_found")), "default Alpha unexpectedly intercepted: %s", short(r.body))
		return "host auth_not_found with no Codex credential; no plugin denial route"
	})
	for _, p := range []string{"GET /v1/realtime", "POST /v1/realtime", "POST /v1/live"} {
		fields := strings.SplitN(p, " ", 2)
		r, err := h.guardRequest(fields[0], fields[1])
		if err == nil {
			h.guardBaseline[p] = r
		}
	}
}
func (h *harness) guardRequest(method, path string) (response, error) {
	headers := map[string]string{}
	if method == "GET" {
		headers = map[string]string{"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ=="}
		path += "?model=gpt-6.1-sol"
	}
	return h.request(method, path, map[string]any{"model": "gpt-6.1-sol", "sdp": "v=0\r\n", "type": "offer"}, h.clientKey, headers)
}
