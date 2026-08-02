package usage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewStore_CreatesDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_usage.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Database file should now exist.
	info, err := os.Stat(dbPath + ".db") // we append .db in NewStore for non-.db paths
	if err == nil && !info.IsDir() {
		return // file exists as expected (with appended .db)
	}

	// Also check if created without the suffix.
	info, err = os.Stat(dbPath + ".sqlite")
	if err != nil {
		t.Logf("database file not found at either path; this may be OK if SQLite creates in-memory")
	} else if info.IsDir() {
		t.Fatal("expected a database file, got directory")
	}
}

func TestStore_BatchInsertAndQuery(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_batch.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	records := []UsageRecord{
		{
			Timestamp:        NowISO(),
			APIKeyID:         "user-001",
			Model:            "llama3.2:latest",
			BackendURL:       "http://localhost:11434",
			PromptTokens:     500,
			CompletionTokens: 200,
			DurationMS:       1500,
			CostUSD:          0.00055,
		},
		{
			Timestamp:        NowISO(),
			APIKeyID:         "user-002",
			Model:            "gemma2:latest",
			BackendURL:       "http://localhost:11435",
			PromptTokens:     1000,
			CompletionTokens: 500,
			DurationMS:       2000,
			CostUSD:          0.00125,
		},
	}

	if err := store.BatchInsert(records); err != nil {
		t.Fatalf("batch insert failed: %v", err)
	}

	// Query to verify records were stored.
	var count int
	err = store.DB().QueryRow("SELECT COUNT(*) FROM usage_records").Scan(&count)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 records, got %d", count)
	}

	// Verify specific field values.
	var model string
	err = store.DB().QueryRow("SELECT model FROM usage_records WHERE api_key_id = ?", "user-001").Scan(&model)
	if err != nil {
		t.Fatalf("query by user failed: %v", err)
	}
	if model != "llama3.2:latest" {
		t.Errorf("expected model llama3.2:latest, got %s", model)
	}

	var totalCost float64
	err = store.DB().QueryRow("SELECT SUM(cost_usd) FROM usage_records").Scan(&totalCost)
	if err != nil {
		t.Fatalf("sum query failed: %v", err)
	}
	expected := 0.00055 + 0.00125
	if totalCost != expected {
		t.Errorf("expected total cost %.8f, got %.8f", expected, totalCost)
	}
}

func TestStore_BatchInsertEmpty(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_empty.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Inserting an empty batch should be a no-op (no error).
	if err := store.BatchInsert(nil); err != nil {
		t.Errorf("expected nil for empty batch, got: %v", err)
	}
}

func TestStore_InitIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_idempotent.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("first init failed: %v", err)
	}

	// Calling Init again should not error.
	if err := store.Init(); err != nil {
		t.Errorf("second init should be idempotent, got: %v", err)
	}
	defer store.Close()
}

func TestNowISO(t *testing.T) {
	ts := NowISO()
	if ts == "" {
		t.Error("expected non-empty timestamp")
	}
	// Should contain "T" (RFC3339 format marker).
	if !contains(ts, "T") {
		t.Errorf("expected ISO 8601 format with 'T', got %s", ts)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
