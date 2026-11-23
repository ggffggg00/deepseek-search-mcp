package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/beareravov/deepseek-search-mcp/internal/deepseek"
)

const (
	MaxQueryRunes    = 128
	MaxURLLength     = 8192
	DefaultSearchHit = 10
	MaxSearchHit     = 50
)

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type searchArgs struct {
	Query        string `json:"query"`
	Count        int    `json:"count"`
	OriQuestion  string `json:"ori_question"`
	Localization string `json:"localization"`
	NeedContent  bool   `json:"need_content"`
}

type fetchArgs struct {
	URL string `json:"url"`
}

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "web_search",
			"title":       "Web Search",
			"description": "Search the live web with the DeepSeek Search API and return ranked results with titles, URLs, snippets, long summaries, optional page content, publication dates and authority scores. Supports search operators such as site:, -site:, \"exact phrase\", -\"phrase\", lang:, after: and before:.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query, 1 to 128 characters. Search operators are supported.",
						"minLength":   1,
						"maxLength":   MaxQueryRunes,
					},
					"count": map[string]any{
						"type":        "integer",
						"description": "Number of results to return, between 1 and 50.",
						"minimum":     1,
						"maximum":     MaxSearchHit,
						"default":     DefaultSearchHit,
					},
					"ori_question": map[string]any{
						"type":        "string",
						"description": "Original user question that triggered the search, up to 128 characters. Improves query understanding.",
						"maxLength":   MaxQueryRunes,
					},
					"localization": map[string]any{
						"type":        "string",
						"description": "Optional IP address used to localize search results.",
					},
					"need_content": map[string]any{
						"type":        "boolean",
						"description": "Return full page content for every result. Increases response size.",
						"default":     false,
					},
				},
				"required":             []string{"query"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "web_fetch",
			"title":       "Fetch Web Page",
			"description": "Fetch a single web page with the DeepSeek Fetch API and return its content converted to Markdown.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "Absolute HTTP or HTTPS URL of the page to fetch.",
					},
				},
				"required":             []string{"url"},
				"additionalProperties": false,
			},
		},
	}
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) *response {
	var params toolCallParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return errorResponse(nil, CodeInvalidParams, "invalid tools/call params: "+err.Error())
		}
	}
	if params.Name == "" {
		return errorResponse(nil, CodeInvalidParams, "tools/call requires a tool name")
	}
	switch params.Name {
	case "web_search":
		return s.webSearch(ctx, params.Arguments)
	case "web_fetch":
		return s.webFetch(ctx, params.Arguments)
	default:
		return errorResponse(nil, CodeInvalidParams, "unknown tool: "+params.Name)
	}
}

func (s *Server) webSearch(ctx context.Context, raw json.RawMessage) *response {
	var args searchArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return toolError(fmt.Errorf("invalid arguments: %w", err))
		}
	}
	req, err := buildSearchRequest(args)
	if err != nil {
		return toolError(err)
	}
	if s.client == nil {
		return toolError(errors.New("web search is unavailable: no API client configured"))
	}
	result, err := s.client.Search(ctx, req)
	if err != nil {
		return toolError(err)
	}
	s.logger.Debug("web search completed", "query", req.Query, "results", len(result.Results))
	text, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return toolError(fmt.Errorf("encode results: %w", err))
	}
	return toolStructured(string(text), result)
}

func (s *Server) webFetch(ctx context.Context, raw json.RawMessage) *response {
	var args fetchArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return toolError(fmt.Errorf("invalid arguments: %w", err))
		}
	}
	target, err := validateFetchURL(args.URL)
	if err != nil {
		return toolError(err)
	}
	if s.client == nil {
		return toolError(errors.New("web fetch is unavailable: no API client configured"))
	}
	page, err := s.client.Fetch(ctx, target)
	if err != nil {
		return toolError(err)
	}
	s.logger.Debug("web fetch completed", "url", target, "content_bytes", len(page.Content))
	text, err := json.MarshalIndent(page, "", "  ")
	if err != nil {
		return toolError(fmt.Errorf("encode page: %w", err))
	}
	return toolStructured(string(text), page)
}

func buildSearchRequest(args searchArgs) (deepseek.SearchRequest, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return deepseek.SearchRequest{}, errors.New("query must not be empty")
	}
	if n := utf8.RuneCountInString(query); n > MaxQueryRunes {
		return deepseek.SearchRequest{}, fmt.Errorf("query must be at most %d characters, got %d", MaxQueryRunes, n)
	}
	oriQuestion := strings.TrimSpace(args.OriQuestion)
	if n := utf8.RuneCountInString(oriQuestion); n > MaxQueryRunes {
		return deepseek.SearchRequest{}, fmt.Errorf("ori_question must be at most %d characters, got %d", MaxQueryRunes, n)
	}
	count := args.Count
	switch {
	case count == 0:
		count = DefaultSearchHit
	case count < 0 || count > MaxSearchHit:
		return deepseek.SearchRequest{}, fmt.Errorf("count must be between 1 and %d, got %d", MaxSearchHit, count)
	}
	return deepseek.SearchRequest{
		Query:        query,
		Count:        count,
		OriQuestion:  oriQuestion,
		Localization: strings.TrimSpace(args.Localization),
		NeedContent:  args.NeedContent,
	}, nil
}

func validateFetchURL(raw string) (string, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", errors.New("url must not be empty")
	}
	if len(target) > MaxURLLength {
		return "", fmt.Errorf("url must be at most %d characters, got %d", MaxURLLength, len(target))
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("url must use the http or https scheme")
	}
	if parsed.Host == "" {
		return "", errors.New("url must include a host")
	}
	if parsed.User != nil {
		return "", errors.New("url must not contain credentials")
	}
	return parsed.String(), nil
}

func toolStructured(text string, structured any) *response {
	return &response{Result: map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"structuredContent": structured,
		"isError":           false,
	}}
}

func toolError(err error) *response {
	return &response{Result: map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": err.Error()},
		},
		"isError": true,
	}}
}
