package dashboard

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/config"
)

func TestDashboardLoginFlow(t *testing.T) {
	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434"}},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local"}}},
		}},
		Users: map[string]config.UserConfig{
			"demo": {APIKeyHash: auth.HashAPIKey("demo-key")},
		},
	}
	authStore := auth.NewStore(cfg)
	handler, err := NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	resp := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	handler.ServeHTTP(resp, request)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden for missing token, got %d", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "Admin Login") {
		t.Fatalf("expected login page, got %q", resp.Body.String())
	}

	form := url.Values{}
	form.Set("token", "super-secret")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResp := httptest.NewRecorder()
	handler.ServeHTTP(loginResp, loginReq)
	if loginResp.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after login, got %d", loginResp.Code)
	}

	cookies := loginResp.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected session cookie to be set")
	}

	overviewReq := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	overviewReq.AddCookie(cookies[0])
	overviewResp := httptest.NewRecorder()
	handler.ServeHTTP(overviewResp, overviewReq)
	if overviewResp.Code != http.StatusOK {
		t.Fatalf("expected overview page after login, got %d", overviewResp.Code)
	}
	if !strings.Contains(overviewResp.Body.String(), "Overview") {
		t.Fatalf("expected overview content, got %q", overviewResp.Body.String())
	}
}

func TestBackendToggle(t *testing.T) {
	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434"}},
		Models:   config.ModelCatalog{Models: map[string]config.ModelEntry{"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local"}}}}},
		Users:    map[string]config.UserConfig{"demo": {APIKeyHash: auth.HashAPIKey("demo-key")}},
	}
	authStore := auth.NewStore(cfg)
	handler, err := NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	loginForm := url.Values{}
	loginForm.Set("token", "super-secret")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(loginForm.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResp := httptest.NewRecorder()
	handler.ServeHTTP(loginResp, loginReq)
	cookie := loginResp.Result().Cookies()[0]

	request := httptest.NewRequest(http.MethodPatch, "/admin/backends/toggle/local", nil)
	request.AddCookie(cookie)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, request)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected toggle success, got %d", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "disabled") {
		t.Fatalf("expected disabled status in body, got %q", resp.Body.String())
	}
}
