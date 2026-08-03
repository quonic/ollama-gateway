# Runtime State and Admin-Edit Semantics Specification

## 1. Purpose

This document defines the gateway’s runtime state model, including which state is held in memory, which is persisted, and how admin-driven runtime edits behave.

## 2. Runtime State Inventory

The gateway maintains the following runtime state:

- API key authentication context
- Per-key token buckets
- Backend health status and disable flags
- Model-resolution cache and backend pools
- In-flight request metadata
- Buffered usage records waiting for persistence

## 3. State Lifecycle

### 3.1 Startup

Runtime state is initialized from configuration and empty defaults.

### 3.2 Request Handling

State is read and updated during auth, rate limiting, model resolution, routing, and usage logging.

### 3.3 Shutdown

In-memory state is discarded unless explicitly flushed to persistent storage.

## 4. Persistence Scope

| Runtime state         | Persisted? | Notes                           |
| --------------------- | ---------- | ------------------------------- |
| Token buckets         | No         | Reset on restart                |
| Backend health        | No         | Recomputed on next health check |
| Disabled backends     | No         | Reverts on restart              |
| Usage records         | Yes        | SQLite                          |
| Runtime model aliases | No         | Unless later persisted          |

## 5. Dashboard Edit Semantics

Runtime edits performed by the dashboard must be clearly documented as non-persistent unless a later phase adds persistence.

### 5.1 Backend Toggle

- Toggle actions update in-memory state only.
- The change is visible immediately in the dashboard.
- The change is not written to disk and is lost on restart.

### 5.2 Model or Alias Changes

- Changes affect the running process immediately.
- They are not persisted to the config file in v1.
- The dashboard should display a banner warning about non-persistent edits.

## 6. Concurrency Expectations

- Runtime state must be safe for concurrent access.
- Mutexes or atomic operations should protect shared structures.
- Background goroutines should not block the request path unnecessarily.

## 7. Restart Behavior

On restart:

- Config is reloaded from file.
- Runtime state is rebuilt from scratch.
- Prior in-memory dashboard edits are discarded.
