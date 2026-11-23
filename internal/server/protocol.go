package server

import "encoding/json"

const (
	JSONRPCVersion            = "2.0"
	LatestProtocolVersion     = "2025-06-18"
	supportedProtocolVersion1 = "2024-11-05"
	supportedProtocolVersion2 = "2025-03-26"
	ServerName                = "deepseek-search-mcp"
)

const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

var supportedProtocolVersions = []string{
	supportedProtocolVersion1,
	supportedProtocolVersion2,
	LatestProtocolVersion,
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
