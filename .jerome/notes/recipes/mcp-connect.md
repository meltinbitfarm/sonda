# Connect Claude to Sonda's MCP server

Verified 2026-11-04 against a local `sonda serve` (curl part, inside the golang:1.27-alpine container).
The claude.ai and Claude Code parts follow their documented commands and have not been tried against a
deployed Sonda yet (no deploy exists, O4).

Endpoint: `https://<host>/mcp/sonda` — Streamable HTTP, stateless, JSON responses (POST only; GET returns 405).
Auth (A4): one secret, `SONDA_MCP_TOKEN`, sent as `Authorization: Bearer <token>` or as `?token=<token>`.

## 1. Make the token

    openssl rand -hex 32      # 64 hex characters; serve refuses anything under 32 characters

Put it in the app's environment (Coolify: environment variable `SONDA_MCP_TOKEN`, marked secret).
`sonda serve` exits with `SONDA_MCP_TOKEN is not set` if it is missing. Changing the token = change the
variable and restart; every client must then be updated.

## 2. Claude Code

Header form (the token does not end up in URLs or proxy logs):

    claude mcp add --transport http sonda https://<host>/mcp/sonda \
      --header "Authorization: Bearer <token>"

Add `--scope user` to have it in every project instead of only the current one. Check with
`claude mcp list` (should show `sonda` connected) and, inside a session, `/mcp`.

## 3. claude.ai (and Claude desktop/mobile, which share the connectors)

Settings → Connectors → Add custom connector:
- Name: `Sonda`
- URL: `https://<host>/mcp/sonda?token=<token>`
- Leave the OAuth client id/secret (advanced settings) empty.

The remote connector only takes a URL, hence `?token=` (same as Pulso). Sonda strips the token from the
URL before the MCP handler and never logs query strings, but a reverse proxy in front of it (Coolify's
Traefik/Caddy) may write access logs with the full URL: keep proxy access logs off for this service, or
the token sits in those logs.

## 4. Smoke test with curl

    URL=https://<host>/mcp/sonda      # or http://127.0.0.1:8080/mcp/sonda locally
    TOKEN=<token>
    # Headers written out on every call: works the same in zsh and bash.

    # 401 without a token
    curl -s -o /dev/null -w '%{http_code}\n' -X POST "$URL" -H 'Content-Type: application/json' -d '{}'

    # initialize: server info, capabilities, instructions
    curl -s "$URL" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -H "Authorization: Bearer $TOKEN" \
      -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'

    # list the tools (here with the token in the URL, as claude.ai does)
    curl -s "$URL?token=$TOKEN" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
      -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'

    # call a tool
    curl -s "$URL" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -H "Authorization: Bearer $TOKEN" -H 'MCP-Protocol-Version: 2025-06-18' \
      -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_apps","arguments":{}}}'

    curl -s "$URL" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -H "Authorization: Bearer $TOKEN" -H 'MCP-Protocol-Version: 2025-06-18' \
      -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"event_counts","arguments":{"app":"Demo","group_by":["name"],"bucket":"day"}}}'

Expected: 401 for the first call; then JSON-RPC responses (`"result":{...}`). A tool call answers with
`content[0].text` and `structuredContent` holding the same JSON; a bad argument answers with
`"isError":true` and a message (e.g. the list of available apps), not an HTTP error. The server is
stateless, so `initialize` is not required before `tools/list` or `tools/call` from curl.

Local run (no TLS) inside the Go container: build and start `sonda serve` as in
notes/recipes/go-via-docker.md, `apk add curl`, then use `URL=http://127.0.0.1:8080/mcp/sonda`.

## Tools

list_apps, event_counts, funnel, retention, recent_events, query (read-only SQL). Arguments and semantics
are in each tool's description (what Claude reads); summary in notes/step3-mcp.md.
