# deepseek-search-mcp

An MCP server that gives agents real web access through the DeepSeek Search and Fetch APIs: one binary, no dependencies, works over stdio and HTTP.

[![Release](https://img.shields.io/github/v/release/ggffggg00/deepseek-search-mcp)](https://github.com/ggffggg00/deepseek-search-mcp/releases) [![Go Version](https://img.shields.io/github/go-mod/go-version/ggffggg00/deepseek-search-mcp)](https://go.dev/)

Agents are good at reasoning and bad at knowing what happened last week. This server hands them two tools — search the web and read a page — backed by DeepSeek's search index. The project is a single static Go binary, so there is no Node runtime, no Python venv, and no `npx` in the startup path.

## Install

Grab the archive for your platform from [Releases](https://github.com/beareravov/deepseek-search-mcp/releases). Every release ships Linux, macOS, and Windows builds for both `amd64` and `arm64`, named `deepseek-search-mcp_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows), with `checksums.txt` next to them.

With the GitHub CLI:

```sh
gh release download --repo beareravov/deepseek-search-mcp --pattern '*_darwin_arm64.tar.gz' --dir .
tar -xzf deepseek-search-mcp_*_darwin_arm64.tar.gz
sudo install -m 755 deepseek-search-mcp /usr/local/bin/
```

Change `darwin_arm64` to `linux_amd64`, `windows_amd64`, or whatever matches your machine. To check the archive against the published checksums:

```sh
shasum -a 256 -c checksums.txt --ignore-missing
```

If Go is already installed, one line does the same job:

```sh
go install github.com/beareravov/deepseek-search-mcp/cmd/deepseek-search-mcp@latest
```

Or build from a checkout:

```sh
git clone https://github.com/beareravov/deepseek-search-mcp.git
cd deepseek-search-mcp
make build
```

The binary lands in `bin/deepseek-search-mcp`.

You need a DeepSeek API key from [platform.deepseek.com](https://platform.deepseek.com/). Search and Fetch are paid endpoints — see [Costs and limits](#costs-and-limits) before pointing a busy agent at them.

## Running it

stdio is the default, which is what most MCP clients expect:

```sh
DEEPSEEK_API_KEY=sk-... deepseek-search-mcp
# or explicitly
DEEPSEEK_API_KEY=sk-... deepseek-search-mcp stdio
```

HTTP mode:

```sh
DEEPSEEK_API_KEY=sk-... deepseek-search-mcp serve --addr 127.0.0.1:8080
```

The endpoint is `POST http://127.0.0.1:8080/mcp`, health check is at `/healthz`. Logs go to stderr, so stdout stays clean for the protocol.

## Connecting a client

### Claude Code

```sh
claude mcp add deepseek-search \
  -e DEEPSEEK_API_KEY=sk-... \
  -- /absolute/path/to/deepseek-search-mcp stdio
```

Add `--scope project` if the server should be shared through the repo's `.mcp.json` instead of your user config.

If you already run the binary in HTTP mode:

```sh
claude mcp add --transport http deepseek-search http://127.0.0.1:8080/mcp
```

### Claude Desktop

Put this in `claude_desktop_config.json` (macOS: `~/Library/Application Support/Claude/`) and restart the app:

```json
{
  "mcpServers": {
    "deepseek-search": {
      "command": "/absolute/path/to/deepseek-search-mcp",
      "args": ["stdio"],
      "env": {
        "DEEPSEEK_API_KEY": "sk-..."
      }
    }
  }
}
```

### Cursor

`~/.cursor/mcp.json` for a global server, `.cursor/mcp.json` inside a project for a shared one:

```json
{
  "mcpServers": {
    "deepseek-search": {
      "command": "/absolute/path/to/deepseek-search-mcp",
      "args": ["stdio"],
      "env": {
        "DEEPSEEK_API_KEY": "sk-..."
      }
    }
  }
}
```

Cursor also accepts a remote server directly:

```json
{
  "mcpServers": {
    "deepseek-search": {
      "url": "http://127.0.0.1:8080/mcp"
    }
  }
}
```

### VS Code

Create `.vscode/mcp.json`. This variant asks for the key on first start instead of storing it in the file:

```json
{
  "inputs": [
    {
      "id": "deepseek-key",
      "type": "promptString",
      "description": "DeepSeek API key",
      "password": true
    }
  ],
  "servers": {
    "deepseek-search": {
      "type": "stdio",
      "command": "/absolute/path/to/deepseek-search-mcp",
      "args": ["stdio"],
      "env": {
        "DEEPSEEK_API_KEY": "${input:deepseek-key}"
      }
    }
  }
}
```

### Any other stdio client

Nearly every MCP client uses the same three fields — command, arguments, environment — and the server needs nothing beyond them. Point the command at the binary, pass `stdio` as the argument, put the API key in the environment:

```json
{
  "mcpServers": {
    "deepseek-search": {
      "command": "/absolute/path/to/deepseek-search-mcp",
      "args": ["stdio"],
      "env": {
        "DEEPSEEK_API_KEY": "sk-..."
      }
    }
  }
}
```

After `go install` the binary is already on `PATH`, so `"command": "deepseek-search-mcp"` works without a path. Clients differ in the wrapper around these fields: Zed calls the section `context_servers`, older ones want `"type": "stdio"` or an `"alwaysAllow"` list. The command, args, and env stay the same.

## Tools

### `web_search`

Searches the live web and returns ranked results. Search syntax is passed through as-is, so operators work: `site:`, `-site:`, `"exact phrase"`, `-"phrase"`, `lang:zh,en`, `after:2025-01-01`, `before:2026-06-01`. Multiple `site:` clauses combine with `OR`.

| Argument | Type | Default | Notes |
| --- | --- | --- | --- |
| `query` | string | required | 1–128 characters, operators allowed |
| `count` | integer | 10 | 1–50 |
| `ori_question` | string | — | original user question, helps query understanding |
| `localization` | string | — | IP address used to localize results |
| `need_content` | boolean | false | return full page content for every hit |

Each result carries `title`, `url`, `snippet`, `summary` (600–800 characters, meant for the model), `content` (only with `need_content`), `site_name`, `site_icon`, `date`, `authority` (0–4, higher is better), and `rerank_score` (above 1.5 is usually worth reading).

```json
{
  "query": "golang mcp server",
  "count": 5,
  "need_content": false
}
```

The tool returns the results as a text block plus `structuredContent`, so both the model and your UI can use them.

### `web_fetch`

Reads a single page and converts it to Markdown. This is the follow-up call after search, or a direct call when the agent already has a URL.

| Argument | Type | Notes |
| --- | --- | --- |
| `url` | string | absolute `http`/`https` URL, up to 8192 characters, no credentials in the URL |

```json
{ "url": "https://go.dev/blog/error-syntax" }
```

Response:

```json
{
  "title": "Error Syntax",
  "content": "# Error Syntax\n\nMarkdown of the page..."
}
```

Fetch depends on the target site being reachable and permissive, so expect occasional `404`-style failures on paywalled or bot-hostile pages.

## Configuration

Environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `DEEPSEEK_API_KEY` | — | API key, required for tool calls |
| `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` | API base URL |
| `DEEPSEEK_ADDR` | `127.0.0.1:8080` | listen address for HTTP mode |
| `DEEPSEEK_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Flags override the environment:

```
deepseek-search-mcp [stdio|serve|version] [flags]

  --transport string   stdio or http
  --addr string        listen address for the http transport
  --api-key string     DeepSeek API key
  --base-url string    DeepSeek API base URL
  --timeout duration   per-request timeout (default 60s)
  --log-level string   debug, info, warn, error
  --version            print version and exit
```

To probe HTTP mode by hand:

```sh
curl -s http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}'
```

## Protocol behaviour

- Supported MCP versions: `2024-11-05`, `2025-03-26`, `2025-06-18`. The server echoes the client's version when it is supported and falls back to `2025-06-18` otherwise.
- Methods: `initialize`, `ping`, `tools/list`, `tools/call`, `resources/list`, `resources/templates/list`, `prompts/list`, `notifications/initialized`, `notifications/cancelled`. JSON-RPC batches are accepted.
- Notifications never get a response; over HTTP that is `202 Accepted`.
- Tool failures come back as a result with `isError: true` and a readable message, while protocol errors use standard JSON-RPC codes (`-32700`, `-32600`, `-32601`, `-32602`, `-32603`).
- `GET /mcp` returns `405`. There is no SSE stream, and the server does not keep session state between requests.
- The HTTP client retries `408`, `429`, and `5xx` up to three times with exponential backoff and honours `Retry-After`. Other `4xx` errors are returned immediately.

## Costs and limits

Search and Fetch are billed per successful request at $0.75 per 1,000 calls each. Failed requests, empty result sets, and timeouts are not charged, and `count` does not change the price. Sending `need_content: true` costs the same as a normal search but produces much larger responses.

Rate limits are per account: 100 QPS for Search, 25 QPS for Fetch, plus a per-domain limit on Fetch. Image search is not available on this API yet.

Details in the [DeepSeek docs](https://api-docs.deepseek.com/guides/search/).
