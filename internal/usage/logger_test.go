package usage

import (
	"testing"
	"time"
)

func TestNewUsageLogger_CreatesAndStarts(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test_logger.db"

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	logger := NewUsageLogger(store, DefaultLoggerOptions())
	if logger == nil {
		t.Fatal("expected non-nil usage logger")
	}
	// Give the background goroutine a moment, then shut down.
	done := make(chan struct{})
	go func() {
		logger.Shutdown(done)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("usage logger shutdown timed out")
	}
}

func TestUsageLogger_LogEnqueuesRecord(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test_enqueue.db"

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	logger := NewUsageLogger(store, LoggerOptions{BufferSize: 10, BatchSize: 50, FlushInterval: 10 * time.Second}) // long flush interval so records stay buffered until shutdown

	// Log a few records. They should be flushed on shutdown (not by the ticker).
	for i := 0; i < 3; i++ {
		logger.Log(UsageRecord{
			Timestamp:        NowISO(),
			APIKeyID:         "user-001",
			Model:            "llama3:latest",
			BackendURL:       "http://localhost:11434",
			PromptTokens:     10 * (i + 1),
			CompletionTokens: 5 * (i + 1),
			DurationMS:       100,
			CostUSD:          0.001,
		})
	}

	// Wait for flush interval to elapse and shutdown drains remaining records.
	done := make(chan struct{})
	go func() {
		logger.Shutdown(done)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown timed out")
	}

	// Verify all records were persisted.
	var count int
	err = store.DB().QueryRow("SELECT COUNT(*) FROM usage_records").Scan(&count)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 records in database, got %d", count)
	}

	var totalPrompt int
	err = store.DB().QueryRow("SELECT SUM(prompt_tokens) FROM usage_records").Scan(&totalPrompt)
	if err != nil {
		t.Fatalf("sum query failed: %v", err)
	}
	expected := 10 + 20 + 30 // i=0→10, i=1→20, i=2→30
	if totalPrompt != expected {
		t.Errorf("expected sum of prompt tokens %d, got %d", expected, totalPrompt)
	}
}

func TestUsageLogger_ShutdownDrainsBufferedRecords(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test_drain.db"

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	logger := NewUsageLogger(store, LoggerOptions{BufferSize: 100, BatchSize: 50, FlushInterval: 10 * time.Second}) // long flush interval so records stay buffered

	for i := 0; i < 5; i++ {
		logger.Log(UsageRecord{
			Timestamp:        NowISO(),
			APIKeyID:         "user-drain",
			Model:            "model-x",
			BackendURL:       "http://backend:11434",
			PromptTokens:     100,
			CompletionTokens: 50,
			DurationMS:       200,
			CostUSD:          0.01,
		})
	}

	done := make(chan struct{})
	go func() {
		logger.Shutdown(done)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown timed out")
	}

	var count int
	err = store.DB().QueryRow("SELECT COUNT(*) FROM usage_records").Scan(&count)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if count != 5 {
		t.Errorf("expected all 5 buffered records to be flushed on shutdown, got %d", count)
	}
}
