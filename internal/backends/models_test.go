package backends

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBackendModelOperations(t *testing.T) {
	var methods []string
	var bodies []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		bodies = append(bodies, string(bodyBytes))

		switch r.URL.Path {
		case "/api/pull":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"success"}`))
		case "/api/delete":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"success"}`))
		case "/api/generate":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"response":"done"}`))
		case "/api/tags":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":[{"name":"llama3.2"}]}`))
		case "/api/ps":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":[{"name":"llama3.2"}]}`))
		case "/api/show":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"model":"llama3.2","details":{"family":"llama"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	backend, err := NewBackend("local", server.URL, 1, "", "/api/version", 5*time.Second, nil)
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}

	if err := backend.PullModel(context.Background(), "llama3.2"); err != nil {
		t.Fatalf("pull model: %v", err)
	}
	if err := backend.DeleteModel(context.Background(), "llama3.2"); err != nil {
		t.Fatalf("delete model: %v", err)
	}
	if err := backend.EjectModel(context.Background(), "llama3.2"); err != nil {
		t.Fatalf("eject model: %v", err)
	}

	models, err := backend.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(models) != 1 || models[0].Name != "llama3.2" {
		t.Fatalf("expected one model, got %#v", models)
	}

	memoryModels, err := backend.ListInMemoryModels(context.Background())
	if err != nil {
		t.Fatalf("list in-memory models: %v", err)
	}
	if len(memoryModels) != 1 || memoryModels[0].Name != "llama3.2" {
		t.Fatalf("expected one in-memory model, got %#v", memoryModels)
	}

	details, err := backend.ShowModel(context.Background(), "llama3.2")
	if err != nil {
		t.Fatalf("show model: %v", err)
	}
	if got := details["model"]; got != "llama3.2" {
		t.Fatalf("expected model details for llama3.2, got %#v", got)
	}

	if len(methods) != 6 {
		t.Fatalf("expected 6 requests, got %d: %v", len(methods), methods)
	}
	if !json.Valid([]byte(bodies[0])) {
		t.Fatalf("expected JSON body for pull, got %q", bodies[0])
	}

	var ejectPayload map[string]any
	if err := json.Unmarshal([]byte(bodies[2]), &ejectPayload); err != nil {
		t.Fatalf("decode eject payload: %v", err)
	}
	if ejectPayload["model"] != "llama3.2" {
		t.Fatalf("expected eject model name llama3.2, got %#v", ejectPayload["model"])
	}
	if ejectPayload["keep_alive"] != float64(0) {
		t.Fatalf("expected keep_alive 0, got %#v", ejectPayload["keep_alive"])
	}
}

func TestPullModelStreamReportsProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pull" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte("{\"status\":\"pulling manifest\"}\n"))
		_, _ = w.Write([]byte("{\"status\":\"downloading\",\"total\":100,\"completed\":25}\n"))
		_, _ = w.Write([]byte("{\"status\":\"success\",\"total\":100,\"completed\":100}\n"))
	}))
	defer server.Close()

	backend, err := NewBackend("local", server.URL, 1, "", "/api/version", 5*time.Second, nil)
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}

	var progressEvents []PullProgress
	if err := backend.PullModelStream(context.Background(), "llama3.2", func(progress PullProgress) {
		progressEvents = append(progressEvents, progress)
	}); err != nil {
		t.Fatalf("pull model stream: %v", err)
	}

	if len(progressEvents) != 3 {
		t.Fatalf("expected 3 progress events, got %d", len(progressEvents))
	}
	if progressEvents[0].Status != "pulling manifest" {
		t.Fatalf("expected first status to be pulling manifest, got %#v", progressEvents[0])
	}
	if progressEvents[1].Completed != 25 || progressEvents[1].Total != 100 {
		t.Fatalf("expected second event to include download progress, got %#v", progressEvents[1])
	}
	if progressEvents[2].Status != "success" || progressEvents[2].Completed != 100 {
		t.Fatalf("expected final success event, got %#v", progressEvents[2])
	}
}

func TestParseModelShowMetadataFullPayload(t *testing.T) {
	payload := map[string]any{
		"capabilities": []any{"completion", "vision"},
		"details": map[string]any{
			"parent_model":       "",
			"format":             "gguf",
			"family":             "gemma4",
			"families":           []any{"gemma4"},
			"parameter_size":     "8.0B",
			"quantization_level": "Q4_K_M",
		},
	}

	metadata := ParseModelShowMetadata(payload)
	if metadata == nil {
		t.Fatalf("expected metadata")
	}
	if len(metadata.Capabilities) != 2 || metadata.Capabilities[0] != "completion" || metadata.Capabilities[1] != "vision" {
		t.Fatalf("unexpected capabilities: %#v", metadata.Capabilities)
	}
	if metadata.Details.Family != "gemma4" {
		t.Fatalf("expected family gemma4, got %q", metadata.Details.Family)
	}
	if metadata.Details.ParameterSize != "8.0B" {
		t.Fatalf("expected parameter_size 8.0B, got %q", metadata.Details.ParameterSize)
	}
	if metadata.Details.QuantizationLevel != "Q4_K_M" {
		t.Fatalf("expected quantization Q4_K_M, got %q", metadata.Details.QuantizationLevel)
	}
	if len(metadata.Details.Families) != 1 || metadata.Details.Families[0] != "gemma4" {
		t.Fatalf("unexpected families: %#v", metadata.Details.Families)
	}
}

func TestParseModelShowMetadataMissingFields(t *testing.T) {
	metadata := ParseModelShowMetadata(map[string]any{
		"model": "llama3.2",
	})

	if metadata == nil {
		t.Fatalf("expected metadata")
	}
	if len(metadata.Capabilities) != 0 {
		t.Fatalf("expected no capabilities, got %#v", metadata.Capabilities)
	}
	if metadata.Details.Family != "" || metadata.Details.Format != "" || metadata.Details.ParameterSize != "" {
		t.Fatalf("expected empty details, got %#v", metadata.Details)
	}
	if metadata.Details.Families == nil {
		t.Fatalf("expected families to be initialized")
	}
}

func TestParseModelShowMetadataIgnoresWrongTypes(t *testing.T) {
	metadata := ParseModelShowMetadata(map[string]any{
		"capabilities": "completion",
		"details": map[string]any{
			"family":   "gemma4",
			"families": "gemma4",
		},
	})

	if metadata == nil {
		t.Fatalf("expected metadata")
	}
	if len(metadata.Capabilities) != 0 {
		t.Fatalf("expected no capabilities for wrong type, got %#v", metadata.Capabilities)
	}
	if metadata.Details.Family != "gemma4" {
		t.Fatalf("expected family gemma4, got %q", metadata.Details.Family)
	}
	if len(metadata.Details.Families) != 0 {
		t.Fatalf("expected no families for wrong type, got %#v", metadata.Details.Families)
	}
}
