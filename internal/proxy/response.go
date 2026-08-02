package proxy

import (
	"bytes"
	"encoding/json"
	"io"
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
	written := 0
	for len(p) > 0 {
		nl := bytes.IndexByte(p, '\n')
		if nl == -1 {
			// No newline — buffer the remainder for next call.
			w.buf = append(w.buf, p...)
			return len(p), nil
		}

		// Build the complete line by prepending any buffered partial content from a previous write.
		var fullLine []byte
		bufLen := len(w.buf)
		if bufLen > 0 {
			fullLine = append(append([]byte{}, w.buf...), p[:nl]...)
			w.buf = w.buf[:0] // clear buffer now that content is copied into fullLine
		} else {
			fullLine = p[:nl]
		}

		// Parse the JSON chunk for token counts (non-fatal: failure just skips extraction).
		var chunk map[string]interface{}
		if json.Unmarshal(fullLine, &chunk) == nil {
			// prompt_eval_count appears in the first non-empty chunk; capture once.
			if w.promptTokens == 0 {
				if pc, ok := chunk["prompt_eval_count"].(float64); ok {
					w.promptTokens = int(pc)
				}
			}
			// eval_count accumulates and is overwritten by later chunks (final value wins).
			if ec, ok := chunk["eval_count"].(float64); ok {
				w.evalTokens = int(ec)
			}
		}

		// Forward the original line + newline to the client unchanged. If there was buffered content,
		// prepend it so nothing is lost between writes.
		toWrite := p[:nl+1]
		if bufLen > 0 {
			toWrite = append(fullLine[:bufLen:bufLen], p[:nl+1]...) // reconstruct buffered + new segment with newline
		}
		n, err := w.w.Write(toWrite)
		if err != nil {
			return written + n, err
		}
		written += n

		p = p[nl+1:]
	}
	return written, nil
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
func writeJSONError(w io.Writer, statusCode int, message string) {
	w.Write([]byte(`{"error":"` + message + `"}`))
}
