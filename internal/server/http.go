package server

import (
	"io"
	"net/http"
)

const (
	MCPPath        = "/mcp"
	HealthPath     = "/healthz"
	maxRequestBody = 1 << 20
)

func (s *Server) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(MCPPath, s.handleMCP)
	mux.HandleFunc(HealthPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	return mux
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed: the MCP endpoint accepts POST only", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if len(body) > maxRequestBody {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	payload := s.Handle(r.Context(), body)
	if payload == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Mcp-Protocol-Version", LatestProtocolVersion)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(payload); err != nil {
		s.logger.Warn("failed to write MCP response", "error", err)
	}
}
