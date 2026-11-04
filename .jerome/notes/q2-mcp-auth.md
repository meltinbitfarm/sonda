# Q2 - MCP auth in Pulso and Celora (read-only analysis)

Both: Laravel MCP `Mcp::web(...)` = streamable HTTP (CLAUDE.md:241 in both), no OAuth (no oauth routes; config/mcp.php redirect_domains is unused boilerplate), no stdio (the .mcp.json in both repos is only laravel-boost dev tool, not the product server).

## Pulso
- Route: pulso/routes/ai.php:7 `Mcp::web('/mcp/pulso', PulsoServer::class)->middleware(EnsureMcpTokenAuthenticated::class)`
- Token sources (app/Http/Middleware/EnsureMcpTokenAuthenticated.php:39-49): query `?token=`, else `Authorization: Bearer`, else header `X-MCP-Token`.
- Comparison: sha256 of token looked up in DB (`where token_hash = hash`, :22-25, McpToken.php:32-35). NOT hash_equals; lookup by hash, so timing leak is of limited use but not constant-time compare.
- Secret: per-user token generated in UI, `pulso_mcp_`+Str::random(48) (McpSettingsController.php:52), stored only as hash (migration 2026_05_15_120000_create_mcp_tokens_table.php:14). No env var.
- 401 JSON `{message}` (:51-56).
- Claude connection: README.md:277-281, remote MCP URL `https://your-domain.com/mcp/pulso?token=YOUR_TOKEN` (Claude.ai remote connector). Bearer documented README.md:246-248.

## Celora
- Route: celora-crm/routes/ai.php:6-7 `Mcp::web('/mcp/celora-crm', CeloraCrm::class)->middleware('auth.mcp')`; alias bootstrap/app.php:37.
- Token (app/Http/Middleware/AuthenticateMcp.php:34): `$request->query('token') ?? $request->bearerToken()`. No X-MCP-Token.
- Comparison: `hash_equals($configuredToken, $token)` (:36) = constant-time.
- Secret: env `MCP_SECRET_TOKEN` (+ `MCP_USER_ID`) via config/mcp.php:33,35; .env.example:67-68. Single static token mapped to one user. 503 if unset (:27-31), 401 otherwise.
- Skips auth if a web session user exists (:20).
- Claude connection: no snippet in README/CLAUDE.md; by implication URL `https://host/mcp/celora-crm?token=...` or Bearer.

## Differences
- Secret: Pulso hashed per-user DB token, Celora single env token.
- Compare: Pulso hash lookup, Celora hash_equals.
- Headers: Pulso also X-MCP-Token.
- Common: query `token` + Bearer; path `/mcp/<name>`; no OAuth.

## Recommendation for Sonda (Go)
Streamable HTTP at `/mcp/sonda`; accept `Authorization: Bearer` and `?token=` (the latter needed for Claude.ai remote connector with URL only; note tokens in URLs can leak into logs, so don't log query strings); single secret from env (e.g. `SONDA_MCP_TOKEN`), compared with `crypto/subtle.ConstantTimeCompare` (on sha256 digests to hide length); 401 JSON on failure; fail closed (503/refuse start) if unset. Matches D7 (one secret read token) and D15. Env var name is Sonda's choice (the two repos' names differ).
