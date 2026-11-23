package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ggffggg00/deepseek-search-mcp/internal/deepseek"
)

type fakeClient struct {
	search     func(context.Context, deepseek.SearchRequest) (deepseek.SearchResponse, error)
	fetch      func(context.Context, string) (deepseek.FetchResponse, error)
	lastSearch deepseek.SearchRequest
	lastFetch  string
	searchHits int
}

func (f *fakeClient) Search(ctx context.Context, req deepseek.SearchRequest) (deepseek.SearchResponse, error) {
	f.searchHits++
	f.lastSearch = req
	if f.search != nil {
		return f.search(ctx, req)
	}
	return deepseek.SearchResponse{}, nil
}

func (f *fakeClient) Fetch(ctx context.Context, target string) (deepseek.FetchResponse, error) {
	f.lastFetch = target
	if f.fetch != nil {
		return f.fetch(ctx, target)
	}
	return deepseek.FetchResponse{}, nil
}

func newTestServer(client Client) *Server {
	return New(Options{Version: "test", Client: client, Logger: slog.New(slog.DiscardHandler)})
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode response: %v (raw=%s)", err, raw)
	}
	return out
}

func resultOf(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	resp := decode(t, raw)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("response has no result: %s", raw)
	}
	return result
}

func errorCodeOf(t *testing.T, raw []byte) int {
	t.Helper()
	resp := decode(t, raw)
	rpcErr, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error: %s", raw)
	}
	code, ok := rpcErr["code"].(float64)
	if !ok {
		t.Fatalf("error has no code: %s", raw)
	}
	return int(code)
}

func TestInitialize(t *testing.T) {
	srv := newTestServer(&fakeClient{})
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"test-client","version":"1.0.0"}}}`)

	payload := srv.Handle(context.Background(), raw)
	resp := decode(t, payload)
	result := resultOf(t, payload)
	if result["protocolVersion"] != "2025-06-18" {
		t.Errorf("unexpected protocol version %v", result["protocolVersion"])
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok || info["name"] != ServerName || info["version"] != "test" {
		t.Errorf("unexpected server info %v", result["serverInfo"])
	}
	if _, ok := result["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("tools capability missing: %v", result["capabilities"])
	}
	if resp["jsonrpc"] != JSONRPCVersion {
		t.Errorf("unexpected jsonrpc version %v", resp["jsonrpc"])
	}
}

func TestInitializeProtocolFallback(t *testing.T) {
	srv := newTestServer(&fakeClient{})
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	result := resultOf(t, srv.Handle(context.Background(), raw))
	if result["protocolVersion"] != LatestProtocolVersion {
		t.Errorf("expected fallback to %s, got %v", LatestProtocolVersion, result["protocolVersion"])
	}
}

func TestToolsList(t *testing.T) {
	srv := newTestServer(&fakeClient{})
	raw := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	result := resultOf(t, srv.Handle(context.Background(), raw))
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("expected two tools, got %v", result["tools"])
	}
	names := make(map[string]bool)
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("unexpected tool entry %v", item)
		}
		if _, ok := tool["inputSchema"].(map[string]any); !ok {
			t.Errorf("tool %v has no input schema", tool["name"])
		}
		name, _ := tool["name"].(string)
		names[name] = true
	}
	if !names["web_search"] || !names["web_fetch"] {
		t.Errorf("unexpected tool names %v", names)
	}
}

func TestWebSearchSuccess(t *testing.T) {
	client := &fakeClient{
		search: func(_ context.Context, req deepseek.SearchRequest) (deepseek.SearchResponse, error) {
			return deepseek.SearchResponse{Results: []deepseek.SearchResult{
				{Title: "DeepSeek", URL: "https://deepseek.com", Summary: "summary", Authority: 4},
			}}, nil
		},
	}
	srv := newTestServer(client)
	raw := []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"web_search","arguments":{"query":"deepseek","need_content":true}}}`)

	result := resultOf(t, srv.Handle(context.Background(), raw))
	if result["isError"] != false {
		t.Errorf("unexpected isError %v", result["isError"])
	}
	if client.lastSearch.Count != DefaultSearchHit {
		t.Errorf("expected default count %d, got %d", DefaultSearchHit, client.lastSearch.Count)
	}
	if client.lastSearch.Query != "deepseek" || !client.lastSearch.NeedContent {
		t.Errorf("unexpected search request %+v", client.lastSearch)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("missing structured content: %v", result)
	}
	results, ok := structured["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("unexpected structured results %v", structured["results"])
	}
}

func TestWebSearchValidation(t *testing.T) {
	client := &fakeClient{}
	srv := newTestServer(client)
	cases := []string{
		`{"query":"   "}`,
		`{"count":5}`,
		`{"query":"ok","count":99}`,
		`{"query":"` + strings.Repeat("a", MaxQueryRunes+1) + `"}`,
	}
	for _, args := range cases {
		raw := []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"web_search","arguments":` + args + `}}`)
		result := resultOf(t, srv.Handle(context.Background(), raw))
		if result["isError"] != true {
			t.Errorf("expected isError for args %s, got %v", args, result["isError"])
		}
	}
	if client.searchHits != 0 {
		t.Errorf("client should not be called, got %d calls", client.searchHits)
	}
}

func TestWebFetchSuccess(t *testing.T) {
	client := &fakeClient{
		fetch: func(_ context.Context, target string) (deepseek.FetchResponse, error) {
			return deepseek.FetchResponse{Title: "Page", Content: "# Page"}, nil
		},
	}
	srv := newTestServer(client)
	raw := []byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"web_fetch","arguments":{"url":"https://example.com/post"}}}`)

	result := resultOf(t, srv.Handle(context.Background(), raw))
	if result["isError"] != false {
		t.Fatalf("unexpected error result %v", result)
	}
	if client.lastFetch != "https://example.com/post" {
		t.Errorf("unexpected fetch url %q", client.lastFetch)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["title"] != "Page" {
		t.Errorf("unexpected structured content %v", result["structuredContent"])
	}
}

func TestWebFetchURLValidation(t *testing.T) {
	client := &fakeClient{}
	srv := newTestServer(client)
	cases := []string{
		`{"url":""}`,
		`{"url":"ftp://example.com/file"}`,
		`{"url":"https://user:pass@example.com/"}`,
		`{"url":"http://"}`,
	}
	for _, args := range cases {
		raw := []byte(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"web_fetch","arguments":` + args + `}}`)
		result := resultOf(t, srv.Handle(context.Background(), raw))
		if result["isError"] != true {
			t.Errorf("expected isError for args %s", args)
		}
	}
	if client.lastFetch != "" {
		t.Errorf("client should not be called, got %q", client.lastFetch)
	}
}

func TestWebSearchClientError(t *testing.T) {
	client := &fakeClient{
		search: func(context.Context, deepseek.SearchRequest) (deepseek.SearchResponse, error) {
			return deepseek.SearchResponse{}, &deepseek.APIError{StatusCode: http.StatusTooManyRequests, Message: "rate limited"}
		},
	}
	srv := newTestServer(client)
	raw := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"web_search","arguments":{"query":"x"}}}`)

	result := resultOf(t, srv.Handle(context.Background(), raw))
	if result["isError"] != true {
		t.Fatalf("expected isError, got %v", result["isError"])
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("unexpected content %v", result["content"])
	}
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "rate limited") {
		t.Errorf("unexpected error text %q", text)
	}
}

func TestUnknownTool(t *testing.T) {
	srv := newTestServer(&fakeClient{})
	raw := []byte(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	if code := errorCodeOf(t, srv.Handle(context.Background(), raw)); code != CodeInvalidParams {
		t.Errorf("expected %d, got %d", CodeInvalidParams, code)
	}
}

func TestUnknownMethod(t *testing.T) {
	srv := newTestServer(&fakeClient{})
	raw := []byte(`{"jsonrpc":"2.0","id":9,"method":"tools/unknown"}`)
	if code := errorCodeOf(t, srv.Handle(context.Background(), raw)); code != CodeMethodNotFound {
		t.Errorf("expected %d, got %d", CodeMethodNotFound, code)
	}
}

func TestParseError(t *testing.T) {
	srv := newTestServer(&fakeClient{})
	if code := errorCodeOf(t, srv.Handle(context.Background(), []byte(`{"jsonrpc":`))); code != CodeParseError {
		t.Errorf("expected %d, got %d", CodeParseError, code)
	}
}

func TestNotificationProducesNoResponse(t *testing.T) {
	srv := newTestServer(&fakeClient{})
	if resp := srv.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); resp != nil {
		t.Errorf("expected no response, got %s", resp)
	}
}

func TestBatchMixesNotificationsAndRequests(t *testing.T) {
	srv := newTestServer(&fakeClient{})
	raw := []byte(`[{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":11,"method":"ping"}]`)
	resp := srv.Handle(context.Background(), raw)
	if resp == nil || resp[0] != '[' {
		t.Fatalf("expected a batch response, got %s", resp)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(resp, &items); err != nil {
		t.Fatalf("decode batch: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one response, got %d", len(items))
	}
}

func TestPing(t *testing.T) {
	srv := newTestServer(&fakeClient{})
	result := resultOf(t, srv.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":12,"method":"ping"}`)))
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}

func TestStdioServe(t *testing.T) {
	srv := newTestServer(&fakeClient{})
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	if err := srv.ServeStdio(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two responses, got %d: %q", len(lines), out.String())
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "{") {
			t.Errorf("unexpected line %q", line)
		}
	}
}

func TestHTTPHandler(t *testing.T) {
	srv := newTestServer(&fakeClient{})
	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()

	post := func(body string) *http.Response {
		resp, err := http.Post(ts.URL+MCPPath, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		return resp
	}

	resp := post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("unexpected status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("unexpected content type %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "protocolVersion") {
		t.Errorf("unexpected body %s", body)
	}

	notif := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	defer notif.Body.Close()
	if notif.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202 for notification, got %d", notif.StatusCode)
	}

	get, err := http.Get(ts.URL + MCPPath)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer get.Body.Close()
	if get.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", get.StatusCode)
	}

	health, err := http.Get(ts.URL + HealthPath)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for health, got %d", health.StatusCode)
	}
}

func TestNilClientReturnsToolError(t *testing.T) {
	srv := New(Options{Version: "test", Logger: slog.New(slog.DiscardHandler)})
	raw := []byte(`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"web_search","arguments":{"query":"x"}}}`)
	result := resultOf(t, srv.Handle(context.Background(), raw))
	if result["isError"] != true {
		t.Fatalf("expected isError, got %v", result)
	}
}
