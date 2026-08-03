# Error Handling and Compatibility Specification

## 1. Purpose

This document defines the gateway’s error contract so that auth, routing, proxying, and dashboard failures are consistent and predictable.

## 2. Standard Error Shape

The gateway should return JSON errors in an Ollama-like format:

```json
{ "error": "message" }
```

For richer cases, the gateway may include a `details` object, but the stable minimum contract is the `error` field.

## 3. Status Code Mapping

| Condition                 | Status         | Notes                       |
| ------------------------- | -------------- | --------------------------- |
| Missing/invalid API key   | `401`          | Auth failure                |
| Unauthorized admin access | `403`          | Distinct from API key auth  |
| Model denied/not allowed  | `403`          | Access control decision     |
| Model not found           | `404`          | Catalog lookup failure      |
| No healthy backends       | `503`          | Routing failure             |
| Rate limit exceeded       | `429`          | Retry-After header included |
| Backend proxy failure     | `502` or `504` | Depends on error type       |

## 4. Layer-Specific Errors

### 4.1 Authentication Errors

- Missing `X-API-Key` → `401 {"error":"missing API key"}`
- Invalid key → `401 {"error":"invalid API key"}`
- Missing/invalid admin token → `403 {"error":"unauthorized"}`

### 4.2 Model Errors

- Unknown model → `404 {"error":"model 'x' not found"}`
- Denied model → `403 {"error":"access to model 'x' is denied"}`
- Not allowed by allow-list → `403 {"error":"model 'x' is not available"}`

### 4.3 Routing Errors

- No healthy backends → `503 {"error":"no healthy backends available"}`
- Selected backend times out → `504 {"error":"upstream timeout"}`

### 4.4 Usage Logging Errors

Usage logging failures must not alter the client response. They should be logged internally and treated as non-fatal.

## 5. Compatibility Expectations

The gateway should preserve the general structure of Ollama responses. For successful requests, the body should be passed through unchanged unless a gateway-level transformation is explicitly required.

## 6. Logging and Observability

Errors should be logged with enough context to debug them without exposing raw keys or sensitive payloads.
