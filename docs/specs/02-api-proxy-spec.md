# API Proxy Specification

## 1. Scope

This document specifies how the Ollama Gateway proxies HTTP requests to backend
Ollama servers. It covers request routing, header handling, streaming behavior, and
response inspection for usage tracking.

## 2. Endpoints Forwarded

All routes under `/api/` are proxied to the selected backend. The gateway does not
define its own API endpoints; it mirrors the Ollama REST API exactly:

| Method | Path               | Description           | Streaming?            |
| ------ | ------------------ | --------------------- | --------------------- |
| POST   | /api/generate      | Generate a completion | Yes (default)         |
| POST   | /api/chat          | Chat completion       | Yes (default)         |
| POST   | /api/embed         | Generate embeddings   | No                    |
| GET    | /api/tags          | List local models     | No                    |
| POST   | /api/show          | Show model info       | No                    |
| DELETE | /api/delete        | Delete a model        | No                    |
| POST   | /api/create        | Create a model        | Yes (status stream)   |
| POST   | /api/pull          | Pull a model          | Yes (progress stream) |
| POST   | /api/push          | Push a model          | Yes (progress stream) |
| GET    | /api/ps            | List running models   | No                    |
| GET    | /api/version       | Server version        | No                    |
| HEAD   | /api/blobs/:digest | Check blob exists     | No                    |
| POST   | /api/blobs/:digest | Push a blob           | No (raw binary body)  |

## 3. Request Routing Logic

### Step 1: Extract Model Name from Request Body

The gateway reads the request body to determine which model is being requested:

- **POST requests with JSON body**: Parse JSON and extract `model` field.
- **GET /api/tags, GET /api/ps, GET /api/version**: No model needed — route based on
  global default backend selection (weighted round-robin).
- **DELETE /api/delete, POST /api/show**: Extract `model` from JSON body.

### Step 2: Resolve Model to Backend(s)

Using the resolved API key's user context and the requested model name:

1. Check per-user allow/deny lists — if denied or not in allow list (when restrict mode is on), return HTTP 403 with an error message listing available models.
2. Apply alias mapping if defined for this user/model combination.
3. Look up which backends serve the resolved model name from the global catalog.
4. If no backends are configured for that model, return HTTP 404 with a descriptive error.

### Step 3: Select Backend via Weighted Round-Robin

From the list of healthy backends serving this model:

1. Use weighted round-robin to select one backend (see `06-backend-routing-spec.md`).
2. If all candidate backends are unhealthy, return HTTP 503 with a message indicating
   no available backends for that model.

### Step 4: Rewrite and Forward Request

- Set the target URL scheme, host, and path to match the selected backend's base URL + original request path.
- Preserve all headers except hop-by-hop headers (handled automatically by `httputil.ReverseProxy`).
- Add `X-Forwarded-For`, `X-Forwarded-Host`, and `X-Forwarded-Proto` headers via `ProxyRequest.SetXForwarded()`.

## 4. Streaming Response Handling

### Ollama's Streaming Format

Ollama streams responses as newline-delimited JSON objects (similar to SSE but without the
event/data prefix). Each line is a complete JSON object:

```json
{"model":"llama3.2","created_at":"...","response":"The","done":false}
{"model":"llama3.2","created_at":"...","response":" sky","done":false}
{"model":"llama3.2","created_at":"...","response":" is blue.","done":true,"eval_count":10,...}
```

### Gateway Behavior for Streaming Responses

The gateway passes streaming responses through to the client without buffering:

- `httputil.ReverseProxy` with `FlushInterval = -1` flushes each chunk immediately upon receipt from backend.
- A custom response writer wrapper intercepts writes to accumulate token counts during streaming.

#### Token Count Capture During Streaming (On-the-fly)

Since Ollama sends one JSON object per line, the gateway wraps the client's `io.Writer`:

```go
type streamingUsageCaptureWriter struct {
    w            io.Writer          // underlying http.ResponseWriter
    buf          []byte             // partial-line buffer
    promptTokens int                // accumulated from first complete chunk
    evalTokens   int                // accumulated (final) count
}

func (w *streamingUsageCaptureWriter) Write(p []byte) (int, error) {
    written := 0
    for len(p) > 0 {
        nl := bytes.IndexByte(p, '\n')
        if nl == -1 {
            w.buf = append(w.buf, p...)
            return len(p), nil
        }
        line := append(w.buf, p[:nl]...)
        w.buf = w.buf[:0]

        // Parse this JSON chunk for token counts
        var chunk map[string]interface{}
        if json.Unmarshal(line, &chunk) == nil {
            // prompt_eval_count appears in the first non-empty chunk
            if pc, ok := chunk["prompt_eval_count"].(float64); ok && w.promptTokens == 0 {
                w.promptTokens = int(pc)
            }
            // eval_count appears in subsequent chunks and accumulates
            if ec, ok := chunk["eval_count"].(float64); ok {
                w.evalTokens = int(ec)
            }
        }

        // Write the original line to client (unchanged)
        n, _ := w.w.Write(p[:nl+1])
        written += n
        p = p[nl+1:]
    }
    return written, nil
}
```

**Key Design Decision**: If on-the-fly capture fails for any reason (malformed JSON line), the request is still proxied correctly — token counts default to 0 in the usage log. The client experience is never degraded by usage tracking.

### Non-Streaming Response Handling

For non-streaming responses (`"stream": false`), Ollama returns a single JSON object:

```json
{
    "model":"llama3.2",
    "response":"The sky is blue.",
    "done":true,
    "prompt_eval_count":15,
    "eval_count":10,
    ...
}
```

The gateway uses `ModifyResponse` to:

1. Read the full response body into memory (responses are small enough).
2. Parse JSON and extract `prompt_eval_count` and `eval_count`.
3. Replace the response body with an identical copy so the client receives unchanged content.
4. Store token counts in a request-scoped context value for the usage logger.

## 5. Header Handling

### Headers Forwarded to Backend (from ReverseProxy defaults)

- All standard headers are forwarded except hop-by-hop headers, which are removed:
  `Connection`, `Keep-Alive`, `Proxy-Authenticate`, `Proxy-Authorization`,
  `TE`, `Trailers`, `Transfer-Encoding`, `Upgrade`.

### Headers Added by Gateway

- `X-Forwarded-For`: Client IP address.
- `X-Forwarded-Host`: Original Host header from client request.
- `X-Forwarded-Proto`: "http" or "https".

## 6. Error Handling

| Scenario                                                       | HTTP Status  | Response Body                           | Notes                                                       |
| -------------------------------------------------------------- | ------------ | --------------------------------------- | ----------------------------------------------------------- |
| Backend unreachable / all backends unhealthy for model         | 503          | `{"error": "..."}` JSON                 | Gateway returns error, does not retry across models.        |
| Model not found in catalog or denied to user                   | 403/404      | `{"error": "..."}` JSON                 | Includes list of available models in message when possible. |
| Backend returns non-2xx status (e.g., Ollama model not loaded) | Pass-through | Original error body from backend        | Gateway forwards the exact response.                        |
| Rate limit exceeded                                            | 429          | `{"error": "rate limit exceeded"}` JSON | Includes `Retry-After` header in seconds.                   |

## 7. Request/Response Logging Policy

### What is Logged (to SQLite usage table)

- Timestamp (UTC)
- API key identifier (hashed or truncated, never raw key)
- Requested model name (user-facing alias if applicable)
- Resolved backend URL
- Prompt token count (`prompt_eval_count` from Ollama response)
- Completion token count (`eval_count`)
- Request duration in milliseconds (from first byte received to last byte sent)
- Calculated cost in USD

### What is NOT Logged

- Raw API keys or admin tokens.
- Full request/response bodies (only metadata extracted for usage tracking).
- Client IP addresses beyond what appears in `X-Forwarded-For` (not stored unless explicitly enabled).

## 8. Compatibility Notes with Ollama API

The gateway aims to be a transparent proxy — clients should not notice any behavioral
differences compared to talking directly to an Ollama server, except:

1. **Authentication**: All `/api/*` requests require `X-API-Key`. This is the only breaking change.
2. **`/api/tags` filtering**: The model list returned by `/api/tags` may be filtered based on
   the user's allowed models — users see only models they have access to, not all backend models.
3. **Error messages**: Gateway-generated errors (auth, rate limit) use JSON format consistent
   with Ollama's error style: `{"error": "..."}`.
