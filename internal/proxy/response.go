package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// UsageStats holds token counts extracted from an Ollama response for usage tracking.
type UsageStats struct {
	PromptTokens int // prompt_eval_count — tokens in the request prompt
	EvalTokens   int // eval_count — tokens generated as completion (final value)
}

// streamingUsageCaptureWriter wraps the client's ResponseWriter to intercept writes,
// accumulate token counts from newline-delimited JSON chunks during streaming responses.
// It passes all bytes through unchanged so the client experience is never affected even if parsing fails.
type streamingUsageCaptureWriter struct {
	w            io.Writer // underlying http.ResponseWriter (or its wrapper)
	buf          []byte    // partial-line buffer for incomplete writes
	promptTokens int       // accumulated from first complete chunk that has prompt_eval_count
	evalTokens   int       // latest eval_count seen across chunks (final value in last "done" chunk)
}

// newStreamingUsageCaptureWriter creates a writer that wraps w and captures usage stats.
func newStreamingUsageCaptureWriter(w io.Writer) *streamingUsageCaptureWriter {
	return &streamingUsageCaptureWriter{w: w}
}

// Write processes the byte slice line-by-line, extracting token counts from each JSON chunk
// before forwarding it to the underlying writer unchanged. Incomplete trailing lines are buffered.
func (w *streamingUsageCaptureWriter) Write(p []byte) (int, error) {
	// Always pass bytes through immediately so Content-Length stays accurate for clients.
	n, err := w.w.Write(p)
	if n <= 0 {
		return n, err
	}

	w.parseForUsage(p[:n])
	return n, err
}

// parseForUsage scans newline-delimited chunks and updates captured token stats.
func (w *streamingUsageCaptureWriter) parseForUsage(p []byte) {
	w.buf = append(w.buf, p...)

	for {
		nl := bytes.IndexByte(w.buf, '\n')
		if nl == -1 {
			return
		}

		line := w.buf[:nl]
		w.buf = w.buf[nl+1:]

		var chunk map[string]interface{}
		if json.Unmarshal(line, &chunk) != nil {
			continue
		}

		if w.promptTokens == 0 {
			if pc, ok := chunk["prompt_eval_count"].(float64); ok {
				w.promptTokens = int(pc)
			}
		}
		if ec, ok := chunk["eval_count"].(float64); ok {
			w.evalTokens = int(ec)
		}
	}
}

// Flush is a no-op pass-through; the underlying ResponseWriter handles actual flushing.
func (w *streamingUsageCaptureWriter) Flush() {}

// stats returns the accumulated UsageStats from streaming capture.
func (w *streamingUsageCaptureWriter) stats() UsageStats {
	return UsageStats{
		PromptTokens: w.promptTokens,
		EvalTokens:   w.evalTokens,
	}
}

// extractNonStreamingUsage parses a complete non-streaming Ollama JSON response body and extracts token counts.
// Returns zero-value UsageStats if parsing fails or fields are absent — never returns an error that affects the client.
func extractNonStreamingUsage(body []byte) UsageStats {
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return UsageStats{}
	}

	stats := UsageStats{}
	if pc, ok := resp["prompt_eval_count"].(float64); ok {
		stats.PromptTokens = int(pc)
	}
	if ec, ok := resp["eval_count"].(float64); ok {
		stats.EvalTokens = int(ec)
	}
	return stats
}

// writeJSONError writes a JSON error response in Ollama's format: {"error": "message"}.
func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	if sri, ok := w.(*streamingResponseInterceptor); ok {
		w = sri.ResponseWriter
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(`{"error":"` + message + `"}`))
}
