package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/ggffggg00/deepseek-search-mcp/internal/deepseek"
)

type Client interface {
	Search(context.Context, deepseek.SearchRequest) (deepseek.SearchResponse, error)
	Fetch(context.Context, string) (deepseek.FetchResponse, error)
}

type Options struct {
	Name    string
	Version string
	Client  Client
	Logger  *slog.Logger
}

type Server struct {
	name    string
	version string
	client  Client
	logger  *slog.Logger
}

func New(opts Options) *Server {
	name := opts.Name
	if name == "" {
		name = ServerName
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Server{
		name:    name,
		version: version,
		client:  opts.Client,
		logger:  logger,
	}
}

func (s *Server) Handle(ctx context.Context, raw []byte) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '[' {
		return s.handleBatch(ctx, trimmed)
	}
	return s.handleMessage(ctx, trimmed)
}

func (s *Server) handleBatch(ctx context.Context, raw []byte) []byte {
	var batch []json.RawMessage
	if err := json.Unmarshal(raw, &batch); err != nil {
		return marshal(errorResponse(nil, CodeParseError, "parse error: "+err.Error()))
	}
	if len(batch) == 0 {
		return marshal(errorResponse(nil, CodeInvalidRequest, "invalid request: empty batch"))
	}
	responses := make([]json.RawMessage, 0, len(batch))
	for _, item := range batch {
		if resp := s.handleMessage(ctx, bytes.TrimSpace(item)); resp != nil {
			responses = append(responses, resp)
		}
	}
	if len(responses) == 0 {
		return nil
	}
	payload, err := json.Marshal(responses)
	if err != nil {
		return marshal(errorResponse(nil, CodeInternalError, "internal error: "+err.Error()))
	}
	return payload
}

func (s *Server) handleMessage(ctx context.Context, raw []byte) []byte {
	var msg message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return marshal(errorResponse(nil, CodeParseError, "parse error: "+err.Error()))
	}
	if msg.JSONRPC != JSONRPCVersion {
		return marshal(errorResponse(msg.ID, CodeInvalidRequest, "invalid request: unsupported jsonrpc version"))
	}
	if msg.Method == "" {
		return marshal(errorResponse(msg.ID, CodeInvalidRequest, "invalid request: method is required"))
	}

	isNotification := len(msg.ID) == 0 || string(msg.ID) == "null"
	resp := s.dispatch(ctx, msg.Method, msg.Params)
	if isNotification {
		return nil
	}
	if resp == nil {
		resp = &response{Result: map[string]any{}}
	}
	resp.JSONRPC = JSONRPCVersion
	resp.ID = msg.ID
	return marshal(resp)
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) *response {
	switch method {
	case "initialize":
		return s.initialize(params)
	case "ping":
		return &response{Result: map[string]any{}}
	case "notifications/initialized", "notifications/cancelled":
		s.logger.Debug("notification received", "method", method)
		return nil
	case "tools/list":
		return &response{Result: map[string]any{"tools": toolDefinitions()}}
	case "tools/call":
		return s.callTool(ctx, params)
	case "resources/list":
		return &response{Result: map[string]any{"resources": []any{}}}
	case "resources/templates/list":
		return &response{Result: map[string]any{"resourceTemplates": []any{}}}
	case "prompts/list":
		return &response{Result: map[string]any{"prompts": []any{}}}
	default:
		return errorResponse(nil, CodeMethodNotFound, "method not found: "+method)
	}
}

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

func (s *Server) initialize(params json.RawMessage) *response {
	var p initializeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return errorResponse(nil, CodeInvalidParams, "invalid initialize params: "+err.Error())
		}
	}
	version := negotiateProtocolVersion(p.ProtocolVersion)
	s.logger.Info("client initialized",
		"client", p.ClientInfo.Name,
		"client_version", p.ClientInfo.Version,
		"protocol_version", version,
	)
	return &response{Result: map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    s.name,
			"version": s.version,
		},
		"instructions": "DeepSeek powered web search and page fetch. Use web_search to find fresh web pages and web_fetch to read a specific page as Markdown.",
	}}
}

func negotiateProtocolVersion(requested string) string {
	for _, supported := range supportedProtocolVersions {
		if supported == requested {
			return requested
		}
	}
	return LatestProtocolVersion
}

func errorResponse(id json.RawMessage, code int, messageText string) *response {
	return &response{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Error:   &rpcError{Code: code, Message: messageText},
	}
}

func marshal(resp *response) []byte {
	if resp == nil {
		return nil
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		fallback := errorResponse(resp.ID, CodeInternalError, "internal error: "+err.Error())
		payload, _ = json.Marshal(fallback)
	}
	return payload
}
