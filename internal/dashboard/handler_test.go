package dashboard

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/config"
	"ollama-gateway/internal/models"
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
	authStore := auth.NewStore(cfg, nil)
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
	if !strings.Contains(overviewResp.Body.String(), "Configured Backends") {
		t.Fatalf("expected overview cards content, got %q", overviewResp.Body.String())
	}
}

func TestBackendToggle(t *testing.T) {
	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434"}},
		Models:   config.ModelCatalog{Models: map[string]config.ModelEntry{"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local"}}}}},
		Users:    map[string]config.UserConfig{"demo": {APIKeyHash: auth.HashAPIKey("demo-key")}},
	}
	authStore := auth.NewStore(cfg, nil)
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

func TestBackendToggle_UpdatesSharedManagerRouting(t *testing.T) {
	cfg := &config.Config{
		Admin: config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{
			{Name: "primary", URL: "http://127.0.0.1:11434", Weight: 1},
			{Name: "secondary", URL: "http://127.0.0.1:11435", Weight: 1},
		},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "primary"}, {Backend: "secondary"}}},
		}},
		Users: map[string]config.UserConfig{"demo": {APIKeyHash: auth.HashAPIKey("demo-key")}},
	}
	authStore := auth.NewStore(cfg, nil)
	resolver, err := models.NewResolver(cfg)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}

	handler, err := NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	handler.SetManager(resolver.Manager())

	loginForm := url.Values{}
	loginForm.Set("token", "super-secret")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(loginForm.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResp := httptest.NewRecorder()
	handler.ServeHTTP(loginResp, loginReq)
	cookie := loginResp.Result().Cookies()[0]

	toggleReq := httptest.NewRequest(http.MethodPatch, "/admin/backends/toggle/secondary", nil)
	toggleReq.AddCookie(cookie)
	toggleResp := httptest.NewRecorder()
	handler.ServeHTTP(toggleResp, toggleReq)
	if toggleResp.Code != http.StatusOK {
		t.Fatalf("expected toggle success, got %d", toggleResp.Code)
	}

	for _, backend := range resolver.Manager().Backends() {
		backend.SetHealth(true)
	}

	pool, err := resolver.Resolve("llama3.2", models.UserOverrides{})
	if err != nil {
		t.Fatalf("resolve model: %v", err)
	}
	selected, err := pool.Select()
	if err != nil {
		t.Fatalf("expected selection to succeed, got %v", err)
	}
	if selected == nil || selected.Name != "primary" {
		t.Fatalf("expected primary backend to remain routable after disabling secondary, got %#v", selected)
	}
}

func TestGenerateKeyWorkflow(t *testing.T) {
	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434"}},
		Models:   config.ModelCatalog{Models: map[string]config.ModelEntry{"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local"}}}}},
		Users:    map[string]config.UserConfig{"demo": {APIKeyHash: auth.HashAPIKey("demo-key")}},
	}
	authStore := auth.NewStore(cfg, nil)
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

	form := url.Values{}
	form.Set("action", "generate")
	req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected generated key page, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "Generated API Key") {
		t.Fatalf("expected generated key UI, got %q", body)
	}
	if !strings.Contains(body, "sha256") {
		t.Fatalf("expected hash output in response, got %q", body)
	}
	if !strings.Contains(body, "Configured API Users") {
		t.Fatalf("expected users page content, got %q", body)
	}
}

func TestCreateUserWorkflow(t *testing.T) {
	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434"}},
		Models:   config.ModelCatalog{Models: map[string]config.ModelEntry{"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local"}}}}},
		Users:    map[string]config.UserConfig{"demo": {APIKeyHash: auth.HashAPIKey("demo-key")}},
	}
	authStore := auth.NewStore(cfg, nil)
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

	form := url.Values{}
	form.Set("action", "create")
	form.Set("user_name", "analytics-team")
	req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected create user page, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "analytics-team") || !strings.Contains(body, "created.") {
		t.Fatalf("expected create user success message, got %q", body)
	}
	if _, ok := cfg.Users["analytics-team"]; !ok {
		t.Fatalf("expected newly created user to be persisted in store")
	}
}

func TestAdminPagesRenderOwnContent(t *testing.T) {
	cfg := &config.Config{
		Admin: config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{
			{Name: "local", URL: "http://127.0.0.1:11434"},
		},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local"}}},
		}},
		Users: map[string]config.UserConfig{
			"demo": {APIKeyHash: auth.HashAPIKey("demo-key")},
		},
	}
	authStore := auth.NewStore(cfg, nil)
	handler, err := NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	tests := []struct {
		name           string
		path           string
		mustContain    string
		mustNotContain string
	}{
		{name: "overview", path: "/admin/overview", mustContain: "Configured Backends", mustNotContain: "Generated API Key"},
		{name: "models", path: "/admin/models", mustContain: "Model Catalog", mustNotContain: "Backend Controls"},
		{name: "backends", path: "/admin/backends", mustContain: "Backend Controls", mustNotContain: "Configured API Users"},
		{name: "users", path: "/admin/users", mustContain: "Configured API Users", mustNotContain: "Filtered Analytics"},
		{name: "logs", path: "/admin/logs", mustContain: "Filtered Analytics", mustNotContain: "Model Catalog"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("X-Admin-Token", "super-secret")
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)

			if resp.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d", tc.path, resp.Code)
			}

			body := resp.Body.String()
			if !strings.Contains(body, tc.mustContain) {
				t.Fatalf("expected %s to include %q, got %q", tc.path, tc.mustContain, body)
			}
			if strings.Contains(body, tc.mustNotContain) {
				t.Fatalf("expected %s to exclude %q, got %q", tc.path, tc.mustNotContain, body)
			}
		})
	}
}
