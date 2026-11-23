package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/ggffggg00/deepseek-search-mcp/internal/deepseek"
	"github.com/ggffggg00/deepseek-search-mcp/internal/server"
)

var version = "dev"

const (
	defaultAddr  = "127.0.0.1:8080"
	defaultLevel = "info"
)

type config struct {
	command   string
	transport string
	addr      string
	apiKey    string
	baseURL   string
	timeout   time.Duration
	logLevel  string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg, err := parseConfig(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", server.ServerName, err)
		return 2
	}
	if cfg.command == "version" {
		fmt.Println(versionString())
		return 0
	}

	logger := newLogger(cfg.logLevel, os.Stderr)
	client := deepseek.New(
		cfg.apiKey,
		deepseek.WithBaseURL(cfg.baseURL),
		deepseek.WithTimeout(cfg.timeout),
		deepseek.WithLogger(logger),
	)
	srv := server.New(server.Options{
		Version: versionString(),
		Client:  client,
		Logger:  logger,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.apiKey == "" {
		logger.Warn("DEEPSEEK_API_KEY is not set; tool calls will fail until it is configured")
	}

	if cfg.transport == "http" {
		if err := serveHTTP(ctx, srv, cfg.addr, logger); err != nil {
			logger.Error("http transport stopped", "error", err)
			return 1
		}
		return 0
	}

	logger.Info("serving MCP over stdio", "version", versionString())
	if err := srv.ServeStdio(ctx, os.Stdin, os.Stdout); err != nil {
		logger.Error("stdio transport stopped", "error", err)
		return 1
	}
	return 0
}

func parseConfig(args []string) (config, error) {
	cfg := config{
		command:   "stdio",
		transport: "stdio",
		addr:      envOr("DEEPSEEK_ADDR", defaultAddr),
		apiKey:    os.Getenv("DEEPSEEK_API_KEY"),
		baseURL:   envOr("DEEPSEEK_BASE_URL", deepseek.DefaultBaseURL),
		timeout:   deepseek.DefaultTimeout,
		logLevel:  envOr("DEEPSEEK_LOG_LEVEL", defaultLevel),
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cfg.command = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet(server.ServerName, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	transport := fs.String("transport", "", "transport: stdio or http")
	addr := fs.String("addr", "", "listen address for the http transport")
	apiKey := fs.String("api-key", "", "DeepSeek API key (default $DEEPSEEK_API_KEY)")
	baseURL := fs.String("base-url", "", "DeepSeek API base URL (default $DEEPSEEK_BASE_URL)")
	timeout := fs.Duration("timeout", 0, "per-request timeout (default 60s)")
	logLevel := fs.String("log-level", "", "log level: debug, info, warn, error")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "%s %s\n\nUsage:\n  %s [stdio|serve|version] [flags]\n\nCommands:\n  stdio    run the MCP server over stdin/stdout (default)\n  serve    run the MCP server over HTTP\n  version  print version information\n\nFlags:\n", server.ServerName, versionString(), server.ServerName)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(rest, " "))
	}

	switch cfg.command {
	case "stdio", "serve", "http", "version":
	default:
		return config{}, fmt.Errorf("unknown command %q, expected stdio, serve or version", cfg.command)
	}

	if *showVersion {
		cfg.command = "version"
	}
	if *transport != "" {
		cfg.transport = *transport
	}
	if *addr != "" {
		cfg.addr = *addr
	}
	if *apiKey != "" {
		cfg.apiKey = *apiKey
	}
	if *baseURL != "" {
		cfg.baseURL = *baseURL
	}
	if *timeout > 0 {
		cfg.timeout = *timeout
	}
	if *logLevel != "" {
		cfg.logLevel = *logLevel
	}

	if (cfg.command == "serve" || cfg.command == "http") && *transport == "" {
		cfg.transport = "http"
	}
	switch cfg.transport {
	case "stdio", "http":
	default:
		return config{}, fmt.Errorf("unknown transport %q, expected stdio or http", cfg.transport)
	}
	return cfg, nil
}

func serveHTTP(ctx context.Context, srv *server.Server, addr string, logger *slog.Logger) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	httpServer := &http.Server{
		Handler:           srv.HTTPHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.Serve(listener)
	}()

	logger.Info("serving MCP over http",
		"addr", listener.Addr().String(),
		"endpoint", "http://"+listener.Addr().String()+server.MCPPath,
		"health", server.HealthPath,
	)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("shutting down http server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			return err
		}
		return nil
	}
}

func newLogger(level string, out *os.File) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: lvl}))
}

func versionString() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	if info.Main.Version == "" || info.Main.Version == "(devel)" {
		return version
	}
	return info.Main.Version
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
