package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSearchSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
		}
		if r.URL.Path != "/v1/tools/search" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("unexpected authorization header %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var req SearchRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.Query != "golang mcp" || req.Count != 5 || !req.NeedContent {
			t.Errorf("unexpected request %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"rerank_score":2.1,"title":"Go MCP","url":"https://example.com","snippet":"short","summary":"long","content":"body","site_name":"Example","site_icon":null,"date":"2026-06-01T00:00:00+00:00","authority":3}]}`)
	}))
	defer server.Close()

	client := New("test-key", WithBaseURL(server.URL), WithLogger(slog.New(slog.DiscardHandler)))
	resp, err := client.Search(context.Background(), SearchRequest{Query: "golang mcp", Count: 5, NeedContent: true})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected one result, got %d", len(resp.Results))
	}
	if resp.Results[0].Title != "Go MCP" || resp.Results[0].Authority != 3 {
		t.Errorf("unexpected result %+v", resp.Results[0])
	}
}

func TestFetchSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tools/fetch" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"title":"Example","content":"# Example"}`)
	}))
	defer server.Close()

	client := New("test-key", WithBaseURL(server.URL), WithLogger(slog.New(slog.DiscardHandler)))
	page, err := client.Fetch(context.Background(), "https://example.com/article")
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if page.Title != "Example" || page.Content != "# Example" {
		t.Errorf("unexpected page %+v", page)
	}
}

func TestMissingAPIKey(t *testing.T) {
	client := New("", WithLogger(slog.New(slog.DiscardHandler)))
	_, err := client.Search(context.Background(), SearchRequest{Query: "test"})
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey, got %v", err)
	}
}

func TestAPIErrorParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Authentication Fails","type":"authentication_error","param":null,"code":"invalid_api_key"}}`)
	}))
	defer server.Close()

	client := New("bad-key", WithBaseURL(server.URL), WithLogger(slog.New(slog.DiscardHandler)))
	_, err := client.Search(context.Background(), SearchRequest{Query: "test"})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized || apiErr.Code != "invalid_api_key" {
		t.Errorf("unexpected api error %+v", apiErr)
	}
	if apiErr.Error() != "deepseek: HTTP 401: Authentication Fails" {
		t.Errorf("unexpected error string %q", apiErr.Error())
	}
}

func TestRetryOnServerError(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"message":"bad gateway"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"results":[{"title":"ok","url":"https://example.com"}]}`)
	}))
	defer server.Close()

	client := New("test-key", WithBaseURL(server.URL), WithMaxAttempts(3), WithLogger(slog.New(slog.DiscardHandler)))
	client.sleep = func(context.Context, time.Duration) error { return nil }

	resp, err := client.Search(context.Background(), SearchRequest{Query: "retry"})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected two attempts, got %d", calls)
	}
	if len(resp.Results) != 1 || resp.Results[0].Title != "ok" {
		t.Errorf("unexpected results %+v", resp.Results)
	}
}

func TestRetryStopsOnClientError(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"'count' must be between 1 and 50"}}`)
	}))
	defer server.Close()

	client := New("test-key", WithBaseURL(server.URL), WithMaxAttempts(3), WithLogger(slog.New(slog.DiscardHandler)))
	client.sleep = func(context.Context, time.Duration) error { return nil }

	if _, err := client.Search(context.Background(), SearchRequest{Query: "bad"}); err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected a single attempt, got %d", calls)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := New("test-key", WithLogger(slog.New(slog.DiscardHandler)))
	if _, err := client.Search(ctx, SearchRequest{Query: "test"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
