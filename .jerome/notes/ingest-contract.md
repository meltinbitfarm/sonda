# Ingest contract: `POST /v1/events`

Version 1, 2026-11-04. Implemented by `internal/ingest` (validation) and `internal/server/ingest.go` (HTTP).
The Swift SDK (sdk/) implements the client side. Change both in the same commit (D17).

## Request

    POST /v1/events
    Authorization: Bearer sk_...          (the app's ingest key, required)
    Content-Type: application/json        (optional; if present must be application/json, charset allowed)
    Content-Encoding: gzip                (optional; recommended for batches)

Body: a JSON object with one key, `events`, an array of 1 to 100 events.

```json
{
  "events": [
    {
      "name": "paywall.shown",
      "ts": 1793880000000,
      "user_id": "0F8FAD5B-D9CB-469F-A165-70867728950E",
      "session_id": "7C9E6679-7425-40DE-944B-E07FC1F90AE7",
      "app_version": "1.4.0",
      "build": "212",
      "os_version": "26.1",
      "locale": "it_IT",
      "region": "IT",
      "storefront": "ITA",
      "device_model": "iPhone16,2",
      "device_type": "phone",
      "props": {"placement": "onboarding", "variant": "b"}
    }
  ]
}
```

The app is identified by the key only. `app_id`, `received_at` and any other unknown field in an event are
ignored (unknown fields are ignored so a newer SDK can talk to an older server).

## Event fields

| field | required | type | rule |
|---|---|---|---|
| name | yes | string | ≤ 64 chars, `area.action` in snake_case: `^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$` (D11) |
| ts | yes | integer | device time, **milliseconds** since Unix epoch UTC; not more than 24 h ahead of server time, not more than 31 days behind |
| user_id | yes | string | UUID (any case; stored lowercase). Random UUID created at first launch, not IDFV (D10) |
| session_id | yes | string | UUID (any case; stored lowercase). New after 30 min of inactivity (D10) |
| app_version | no | string | CFBundleShortVersionString, ≤ 64 bytes |
| build | no | string | CFBundleVersion, ≤ 64 bytes |
| os_version | no | string | e.g. `26.1`, ≤ 64 bytes |
| locale | no | string | Locale.current.identifier, ≤ 64 bytes |
| region | no | string | Locale.current.region?.identifier, ≤ 64 bytes |
| storefront | no | string | Storefront.current?.countryCode (alpha-3), ≤ 64 bytes; omit if nil |
| device_model | no | string | raw hardware id, e.g. `iPhone16,2`, ≤ 64 bytes |
| device_type | no | string | one of `phone`, `pad`, `mac` |
| props | no | object | flat JSON object, see below; missing or null = `{}` |

Optional strings: omitted, `null` or `""` are all stored as NULL. No control characters allowed.
A field of the wrong JSON type (e.g. `"ts": "2026-…"`, `"ts": 1.5`, `"region": 39`) rejects the event.

### props
- JSON object; values only string, number, boolean or null. No nested objects or arrays (D13).
- Keys snake_case `^[a-z][a-z0-9_]*$`, ≤ 64 chars (matches the D12 catalogue: `step_name`, `product_id`).
- ≤ 2048 bytes in compact JSON encoding (keys sorted, no spaces), which is what is stored.
- Never personal data (D11).

### Why these ts bounds
- 24 h in the future: tolerates a device clock set wrong by hours, rejects garbage such as a
  microsecond or nanosecond timestamp (which would land centuries ahead).
- 31 days in the past: the SDK queues offline (D8), so late delivery is normal; a month covers a phone
  left offline for weeks and the day-30 retention point (D14). Older events would rewrite cohorts that
  have already been read, and a seconds-instead-of-milliseconds bug lands in 1970 and is caught here.
  The SDK should drop queued events older than 30 days instead of sending them.

## Responses

All bodies are JSON. Order of checks: key, rate limit, headers, body size, body shape, events.

| status | when | body | SDK should |
|---|---|---|---|
| 200 | batch processed (even if every event was rejected) | `{"accepted":2,"rejected":1,"errors":[{"index":1,"reason":"name: must be area.action in snake_case"}]}` | remove the whole batch from the queue; rejected events would fail again. Log reasons in DEBUG |
| 400 | body not JSON, not `{"events":[...]}`, empty array, invalid gzip | `{"error":"..."}` | drop the batch (it will never succeed) |
| 401 | missing/malformed Authorization, unknown key | `{"error":"..."}`, `WWW-Authenticate: Bearer` | keep the queue (bounded), stop sending until next launch |
| 413 | more than 100 events, or body > 256 KB (compressed or decompressed) | `{"error":"..."}` | split the batch and retry |
| 415 | Content-Type not JSON, Content-Encoding other than gzip | `{"error":"..."}` | bug: drop |
| 429 | over 60 requests/min for this key | `{"error":"..."}`, `Retry-After: <seconds>` | wait Retry-After, retry same batch |
| 500/503 | storage error | `{"error":"..."}` | retry with backoff |

`errors` lists only rejected events, by zero-based index in the request array, with a short reason that
never echoes the submitted value. It is omitted when nothing was rejected.

Limits (rate, batch size, body size) are server configuration and may differ per deployment; the
SDK should use 100 events/batch and react to 413/429 rather than hard-code the others.

## Privacy (D9)
The server never reads the client address or forwarding headers, stores nothing but the fields above,
and its logs redact anything shaped like an IP address.
