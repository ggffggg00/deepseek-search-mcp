package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL   = "https://api.deepseek.com"
	DefaultTimeout   = 60 * time.Second
	DefaultAttempts  = 3
	maxResponseBytes = 32 << 20
	maxErrorMessage  = 512
)

var ErrMissingAPIKey = errors.New("deepseek: API key is not configured, set DEEPSEEK_API_KEY")

type APIError struct {
	StatusCode int
	Type       string
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("deepseek: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("deepseek: HTTP %d: %s", e.StatusCode, e.Message)
}

type Client struct {
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	logger      *slog.Logger
	attempts    int
	backoffBase time.Duration
	sleep       func(context.Context, time.Duration) error
}

type Option func(*Client)

func WithBaseURL(rawURL string) Option {
	return func(c *Client) {
		if trimmed := strings.TrimSpace(rawURL); trimmed != "" {
			c.baseURL = strings.TrimRight(trimmed, "/")
		}
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		if timeout > 0 {
			c.httpClient.Timeout = timeout
		}
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

func WithLogger(logger *slog.Logger) Option {
	return func(c *Client) {
		if logger != nil {
			c.logger = logger
		}
	}
}

func WithMaxAttempts(attempts int) Option {
	return func(c *Client) {
		if attempts > 0 {
			c.attempts = attempts
		}
	}
}

func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:      strings.TrimSpace(apiKey),
		baseURL:     DefaultBaseURL,
		httpClient:  &http.Client{Timeout: DefaultTimeout},
		logger:      slog.New(slog.DiscardHandler),
		attempts:    DefaultAttempts,
		backoffBase: 500 * time.Millisecond,
		sleep:       sleepContext,
	}
	for _, opt := range opts {
		opt(c)
	}
	c.baseURL = strings.TrimRight(c.baseURL, "/")
	return c
}

func (c *Client) Search(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	var out SearchResponse
	if err := c.post(ctx, "/v1/tools/search", req, &out); err != nil {
		return SearchResponse{}, err
	}
	return out, nil
}

func (c *Client) Fetch(ctx context.Context, target string) (FetchResponse, error) {
	var out FetchResponse
	if err := c.post(ctx, "/v1/tools/fetch", FetchRequest{URL: target}, &out); err != nil {
		return FetchResponse{}, err
	}
	return out, nil
}

func (c *Client) post(ctx context.Context, path string, payload any, dest any) error {
	if c.apiKey == "" {
		return ErrMissingAPIKey
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("deepseek: encode request: %w", err)
	}
	endpoint := c.baseURL + path
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("deepseek: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if attempt < c.attempts {
				delay := c.backoff(attempt, "")
				c.logger.Warn("deepseek request failed, retrying", "path", path, "attempt", attempt, "delay", delay, "error", err)
				if err := c.sleep(ctx, delay); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("deepseek: request failed: %w", err)
		}

		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("deepseek: read response: %w", readErr)
		}
		if resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal(data, dest); err != nil {
				return fmt.Errorf("deepseek: decode response: %w", err)
			}
			c.logger.Debug("deepseek request succeeded", "path", path, "status", resp.StatusCode, "bytes", len(data))
			return nil
		}

		apiErr := parseAPIError(resp.StatusCode, data)
		if attempt < c.attempts && retryableStatus(resp.StatusCode) {
			delay := c.backoff(attempt, resp.Header.Get("Retry-After"))
			c.logger.Warn("deepseek request failed, retrying", "path", path, "status", resp.StatusCode, "attempt", attempt, "delay", delay)
			if err := c.sleep(ctx, delay); err != nil {
				return err
			}
			continue
		}
		return apiErr
	}
}

func (c *Client) backoff(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds > 0 && seconds <= 60 {
		return time.Duration(seconds) * time.Second
	}
	delay := c.backoffBase << (attempt - 1)
	if delay > 8*time.Second {
		delay = 8 * time.Second
	}
	return delay
}

func retryableStatus(status int) bool {
	switch {
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests:
		return true
	case status >= 500:
		return true
	default:
		return false
	}
}

func parseAPIError(status int, body []byte) *APIError {
	result := &APIError{StatusCode: status}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error.Message != "" {
		result.Message = payload.Error.Message
		result.Type = payload.Error.Type
		result.Code = payload.Error.Code
		return result
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed != "" {
		if len(trimmed) > maxErrorMessage {
			trimmed = trimmed[:maxErrorMessage] + "..."
		}
		result.Message = trimmed
	}
	return result
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
