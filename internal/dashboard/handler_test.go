package dashboard

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/backends"
	"ollama-gateway/internal/config"
	"ollama-gateway/internal/models"
	"ollama-gateway/internal/tlsruntime"
	"ollama-gateway/internal/usage"
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

func TestDashboardAuth_AllowsAdminBearerToken(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	req.Header.Set("Authorization", "Bearer super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected overview with admin bearer token, got %d", resp.Code)
	}
}

func TestDashboardAuth_XAdminTokenPrecedenceOverBearer(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	req.Header.Set("X-Admin-Token", "wrong-token")
	req.Header.Set("Authorization", "Bearer super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden when X-Admin-Token is invalid, got %d", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "Admin Login") {
		t.Fatalf("expected login page on auth failure, got %q", resp.Body.String())
	}
}

func TestOverviewShowsTLSStatusCard(t *testing.T) {
	tempDir := t.TempDir()
	certPath := filepath.Join(tempDir, "tls-cert.pem")
	keyPath := filepath.Join(tempDir, "tls-key.pem")
	certPEM, keyPEM := dashboardTestCertPairPEM(t, time.Now().Add(21*24*time.Hour), big.NewInt(91))
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			TLSCertPath:      certPath,
			TLSKeyPath:       keyPath,
			TLSCheckInterval: time.Hour,
		},
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

	mgr := tlsruntime.NewManager(certPath, keyPath, time.Hour, 30, nil)
	if err := mgr.LoadInitial(); err != nil {
		t.Fatalf("load initial cert: %v", err)
	}
	handler.SetTLSManager(mgr)

	req := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected overview page, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "TLS Certificate") {
		t.Fatalf("expected TLS certificate card, got %q", body)
	}
	if !strings.Contains(body, "Expires At") || !strings.Contains(body, "Last Reload") {
		t.Fatalf("expected expiry and reload fields, got %q", body)
	}
}

func TestOverviewShowsConfigReloadStatusCard(t *testing.T) {
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

	handler.SetReloadStatusProvider(func() config.ReloadStatus {
		return config.ReloadStatus{
			LastReloadAt: time.Date(2026, time.August, 3, 18, 20, 0, 0, time.UTC),
			LastError:    "",
			LastTrigger:  "sighup",
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected overview page, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "Config Reload") {
		t.Fatalf("expected config reload card, got %q", body)
	}
	if !strings.Contains(body, "sighup") {
		t.Fatalf("expected trigger value in config reload card, got %q", body)
	}
	if !strings.Contains(body, "2026-08-03T18:20:00Z") {
		t.Fatalf("expected reload timestamp in config reload card, got %q", body)
	}
	if !strings.Contains(body, "none") {
		t.Fatalf("expected default last error value in config reload card, got %q", body)
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

func dashboardTestCertPairPEM(t *testing.T, notAfter time.Time, serial *big.Int) ([]byte, []byte) {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		DNSNames: []string{"localhost"},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("x509.CreateCertificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	return certPEM, keyPEM
}

func TestBackendToggle_PostRedirectsBackToBackendsPage(t *testing.T) {
	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{Name: "local-gpu", URL: "http://127.0.0.1:11434"}},
		Models:   config.ModelCatalog{Models: map[string]config.ModelEntry{"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local-gpu"}}}}},
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

	request := httptest.NewRequest(http.MethodPost, "/admin/backends/toggle/local-gpu", nil)
	request.AddCookie(cookie)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, request)
	if resp.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after form toggle, got %d", resp.Code)
	}
	if location := resp.Header().Get("Location"); location != "/admin/backends" {
		t.Fatalf("expected redirect to /admin/backends, got %q", location)
	}
	if !handler.state.disabledBackends["local-gpu"] {
		t.Fatal("expected backend to be marked disabled after POST toggle")
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

func TestUpdateAndRotateUserWorkflow(t *testing.T) {
	cfg := &config.Config{
		RateLimit: config.RateLimitingConfig{DefaultRate: 10, DefaultBurst: 50, TTL: 1},
		Admin:     config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends:  []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434"}},
		Models:    config.ModelCatalog{Models: map[string]config.ModelEntry{"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local"}}}}},
		Users: map[string]config.UserConfig{
			"demo": {APIKeyHash: auth.HashAPIKey("demo-key")},
		},
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

	updateForm := url.Values{}
	updateForm.Set("action", "update")
	updateForm.Set("user_name", "demo")
	updateForm.Set("model_allow", "llama3.2, qwen2.5")
	updateForm.Set("model_deny", "phi3")
	updateForm.Set("aliases", "chat:llama3.2,coder:qwen2.5")
	updateForm.Set("rate_limit_enabled", "on")
	updateForm.Set("rate_limit_rate", "25.5")
	updateForm.Set("rate_limit_burst", "77")
	updateForm.Set("rate_limit_ttl_seconds", "900")

	updateReq := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(updateForm.Encode()))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateReq.AddCookie(cookie)
	updateResp := httptest.NewRecorder()
	handler.ServeHTTP(updateResp, updateReq)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("expected update user page, got %d", updateResp.Code)
	}
	updateBody := updateResp.Body.String()
	if !strings.Contains(updateBody, "updated") {
		t.Fatalf("expected update success message, got %q", updateBody)
	}

	uc := cfg.Users["demo"]
	if uc.RateLimit == nil || uc.RateLimit.Burst != 77 {
		t.Fatalf("expected updated rate limit burst 77, got %#v", uc.RateLimit)
	}
	if uc.Aliases["chat"] != "llama3.2" {
		t.Fatalf("expected alias chat -> llama3.2, got %#v", uc.Aliases)
	}

	oldHash := uc.APIKeyHash
	rotateForm := url.Values{}
	rotateForm.Set("action", "rotate")
	rotateForm.Set("user_name", "demo")

	rotateReq := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(rotateForm.Encode()))
	rotateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rotateReq.AddCookie(cookie)
	rotateResp := httptest.NewRecorder()
	handler.ServeHTTP(rotateResp, rotateReq)
	if rotateResp.Code != http.StatusOK {
		t.Fatalf("expected rotate user page, got %d", rotateResp.Code)
	}
	rotateBody := rotateResp.Body.String()
	if !strings.Contains(rotateBody, "API key rotated") {
		t.Fatalf("expected rotate success message, got %q", rotateBody)
	}
	if !strings.Contains(rotateBody, "Generated API Key") {
		t.Fatalf("expected one-time generated key to be shown, got %q", rotateBody)
	}

	newHash := cfg.Users["demo"].APIKeyHash
	if newHash == oldHash {
		t.Fatalf("expected API key hash to change on rotation")
	}
	re := regexp.MustCompile(`Raw key:</strong> <span class="mono">([^<]+)</span>`)
	matches := re.FindStringSubmatch(rotateBody)
	if len(matches) < 2 {
		t.Fatalf("expected raw key in response body, got %q", rotateBody)
	}
	if _, ok := authStore.LookupAPIKey(matches[1]); !ok {
		t.Fatalf("expected rotated key to authenticate")
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

func TestLogsPartialRendersFragmentOnly(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/admin/logs/partial", nil)
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for logs partial, got %d", resp.Code)
	}
	if got := resp.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("expected text/html content type, got %q", got)
	}
	if got := resp.Header().Get("HX-Push-Url"); got != "/admin/logs" {
		t.Fatalf("expected HX-Push-Url to point at canonical logs route, got %q", got)
	}

	body := resp.Body.String()
	if !strings.Contains(body, "id=\"logs-results\"") {
		t.Fatalf("expected logs fragment wrapper, got %q", body)
	}
	if !strings.Contains(body, "Filter Records") || !strings.Contains(body, "Usage Records") {
		t.Fatalf("expected logs cards in fragment, got %q", body)
	}
	if strings.Contains(body, "<!doctype html>") || strings.Contains(body, "Control panel") {
		t.Fatalf("expected fragment-only response without layout shell, got %q", body)
	}
}

func TestOverviewPartialRendersFragmentOnly(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/admin/overview/partial?window=7d", nil)
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for overview partial, got %d", resp.Code)
	}
	if got := resp.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("expected text/html content type, got %q", got)
	}
	if got := resp.Header().Get("HX-Push-Url"); got != "/admin/overview?window=7d" {
		t.Fatalf("expected HX-Push-Url for selected window, got %q", got)
	}

	body := resp.Body.String()
	if !strings.Contains(body, "id=\"overview-results\"") {
		t.Fatalf("expected overview fragment wrapper, got %q", body)
	}
	if !strings.Contains(body, "hx-trigger=\"every 10s\"") {
		t.Fatalf("expected periodic refresh trigger, got %q", body)
	}
	if !strings.Contains(body, "Last updated:") {
		t.Fatalf("expected last updated marker in overview fragment, got %q", body)
	}
	if !strings.Contains(body, "<span class=\"mono\">") {
		t.Fatalf("expected monospaced timestamp value, got %q", body)
	}
	if !strings.Contains(body, "window-link is-active") {
		t.Fatalf("expected one active window toggle, got %q", body)
	}
	if strings.Contains(body, "<!doctype html>") || strings.Contains(body, "Control panel") {
		t.Fatalf("expected fragment-only response without layout shell, got %q", body)
	}
}

func TestOverviewPageIncludesPollingVisibilityGuard(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected overview page, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "htmx:beforeRequest") {
		t.Fatalf("expected visibility-aware htmx request guard script, got %q", body)
	}
	if !strings.Contains(body, "document.hidden") {
		t.Fatalf("expected hidden-tab check in script, got %q", body)
	}
	if !strings.Contains(body, "/admin/overview/partial") {
		t.Fatalf("expected overview partial path in script, got %q", body)
	}
}

func TestOverviewWindowFiltersUsageSummary(t *testing.T) {
	dir := t.TempDir()
	usageStore, err := usage.NewStore(filepath.Join(dir, "dashboard-overview-window.db"))
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	t.Cleanup(func() {
		_ = usageStore.Close()
	})

	base := time.Date(2026, time.August, 3, 18, 0, 0, 0, time.UTC)
	records := []usage.UsageRecord{
		{Timestamp: base.Add(-2 * time.Hour).Format(time.RFC3339), APIKeyID: "key-a", Model: "recent-model", BackendURL: "http://127.0.0.1:11434", PromptTokens: 100, CompletionTokens: 20, DurationMS: 100, CostUSD: 1.0},
		{Timestamp: base.Add(-20 * time.Hour).Format(time.RFC3339), APIKeyID: "key-b", Model: "recent-model-2", BackendURL: "http://127.0.0.1:11434", PromptTokens: 100, CompletionTokens: 20, DurationMS: 100, CostUSD: 2.0},
		{Timestamp: base.Add(-36 * time.Hour).Format(time.RFC3339), APIKeyID: "key-c", Model: "older-model", BackendURL: "http://127.0.0.1:11434", PromptTokens: 100, CompletionTokens: 20, DurationMS: 100, CostUSD: 3.0},
	}
	if err := usageStore.BatchInsert(records); err != nil {
		t.Fatalf("seed usage records: %v", err)
	}

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
	handler, err := NewHandler(cfg, authStore, usageStore, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	handler.now = func() time.Time { return base }

	t.Run("24h window excludes older records", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/overview/partial?window=24h", nil)
		req.Header.Set("X-Admin-Token", "super-secret")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("expected overview partial response, got %d", resp.Code)
		}
		body := resp.Body.String()
		if !strings.Contains(body, "Requests (last 24h)") {
			t.Fatalf("expected 24h window label, got %q", body)
		}
		if !strings.Contains(body, "<p class=\"metric\">2</p>") {
			t.Fatalf("expected 2 requests in 24h window, got %q", body)
		}
		if strings.Contains(body, "older-model") {
			t.Fatalf("did not expect older model in 24h window, got %q", body)
		}
	})

	t.Run("7d window includes older records", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/overview/partial?window=7d", nil)
		req.Header.Set("X-Admin-Token", "super-secret")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("expected overview partial response, got %d", resp.Code)
		}
		body := resp.Body.String()
		if !strings.Contains(body, "Requests (last 7d)") {
			t.Fatalf("expected 7d window label, got %q", body)
		}
		if !strings.Contains(body, "<p class=\"metric\">3</p>") {
			t.Fatalf("expected 3 requests in 7d window, got %q", body)
		}
		if !strings.Contains(body, "older-model") {
			t.Fatalf("expected older model in 7d window, got %q", body)
		}
	})
}

func TestLogsPartialHonorsPaginationAndFilters(t *testing.T) {
	dir := t.TempDir()
	usageStore, err := usage.NewStore(filepath.Join(dir, "dashboard-logs-partial.db"))
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	t.Cleanup(func() {
		_ = usageStore.Close()
	})

	records := make([]usage.UsageRecord, 0, 12)
	for i := 1; i <= 12; i++ {
		model := "alpha"
		if i <= 2 {
			model = "beta"
		}
		records = append(records, usage.UsageRecord{
			Timestamp:        fmt.Sprintf("2026-08-03T10:%02d:00Z", i),
			APIKeyID:         fmt.Sprintf("key-%02d", i),
			Model:            model,
			BackendURL:       "http://127.0.0.1:11434",
			PromptTokens:     100 + i,
			CompletionTokens: 20 + i,
			DurationMS:       100 + i,
			CostUSD:          float64(i) * 0.1,
		})
	}
	if err := usageStore.BatchInsert(records); err != nil {
		t.Fatalf("seed usage records: %v", err)
	}

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
	handler, err := NewHandler(cfg, authStore, usageStore, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	t.Run("pagination second page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/logs/partial?page=2", nil)
		req.Header.Set("X-Admin-Token", "super-secret")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("expected page 2 partial response, got %d", resp.Code)
		}
		body := resp.Body.String()
		if !strings.Contains(body, "Page 2") {
			t.Fatalf("expected page indicator for page 2, got %q", body)
		}
		if got := resp.Header().Get("HX-Push-Url"); got != "/admin/logs?page=2" {
			t.Fatalf("expected HX-Push-Url for page 2, got %q", got)
		}
		if !strings.Contains(body, "key-02") || !strings.Contains(body, "key-01") {
			t.Fatalf("expected oldest records on page 2, got %q", body)
		}
		if strings.Contains(body, "key-12") {
			t.Fatalf("did not expect newest record on page 2, got %q", body)
		}
	})

	t.Run("model filter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/logs/partial?page=1&model=beta", nil)
		req.Header.Set("X-Admin-Token", "super-secret")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("expected filtered partial response, got %d", resp.Code)
		}
		body := resp.Body.String()
		if !strings.Contains(body, "value=\"beta\"") {
			t.Fatalf("expected filter value to be preserved, got %q", body)
		}
		if got := resp.Header().Get("HX-Push-Url"); got != "/admin/logs?page=1&model=beta" {
			t.Fatalf("expected HX-Push-Url for filtered request, got %q", got)
		}
		if !strings.Contains(body, ">beta<") {
			t.Fatalf("expected beta model records in filtered response, got %q", body)
		}
		if strings.Contains(body, ">alpha<") {
			t.Fatalf("did not expect alpha model records in filtered response, got %q", body)
		}
	})
}

func TestDeactivateUserWorkflow(t *testing.T) {
	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434"}},
		Models:   config.ModelCatalog{Models: map[string]config.ModelEntry{"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local"}}}}},
		Users: map[string]config.UserConfig{
			"demo":       {APIKeyHash: auth.HashAPIKey("demo-key")},
			"to-disable": {APIKeyHash: auth.HashAPIKey("kill-me")},
		},
	}
	authStore := auth.NewStore(cfg, nil)
	handler, err := NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	form := url.Values{}
	form.Set("action", "deactivate")
	form.Set("user_name", "to-disable")
	req := httptest.NewRequest(http.MethodPost, "/admin/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected deactivate user page, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "to-disable") || !strings.Contains(body, "deactivated") {
		t.Fatalf("expected deactivate success message, got %q", body)
	}
	if _, ok := cfg.Users["to-disable"]; ok {
		t.Fatalf("expected user removed from config map")
	}
	if _, ok := authStore.LookupAPIKey("kill-me"); ok {
		t.Fatalf("expected deactivated user key to fail authentication")
	}
}

func TestUsersPageShowsPerUserStats(t *testing.T) {
	dir := t.TempDir()
	usageStore, err := usage.NewStore(filepath.Join(dir, "dashboard-user-stats.db"))
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	t.Cleanup(func() {
		_ = usageStore.Close()
	})

	if err := usageStore.BatchInsert([]usage.UsageRecord{
		{Timestamp: "2026-08-02T10:00:00Z", APIKeyID: "demo", Model: "llama3.2", BackendURL: "http://127.0.0.1:11434", PromptTokens: 120, CompletionTokens: 30, DurationMS: 120, CostUSD: 0.25},
		{Timestamp: "2026-08-02T11:00:00Z", APIKeyID: "demo", Model: "qwen2.5", BackendURL: "http://127.0.0.1:11434", PromptTokens: 80, CompletionTokens: 20, DurationMS: 110, CostUSD: 0.4},
	}); err != nil {
		t.Fatalf("seed usage records: %v", err)
	}

	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434"}},
		Models:   config.ModelCatalog{Models: map[string]config.ModelEntry{"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local"}}}}},
		Users: map[string]config.UserConfig{
			"demo": {APIKeyHash: auth.HashAPIKey("demo-key")},
		},
	}
	authStore := auth.NewStore(cfg, nil)
	handler, err := NewHandler(cfg, authStore, usageStore, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected users page, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "Usage Snapshot") || !strings.Contains(body, "Top Models by Spend") {
		t.Fatalf("expected per-user stats content, got %q", body)
	}
	if !strings.Contains(body, "$0.65") {
		t.Fatalf("expected aggregated user cost in response, got %q", body)
	}
}

func TestModelCreateWorkflow(t *testing.T) {
	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434"}, {Name: "edge", URL: "http://127.0.0.1:11435"}},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local", Weight: 1}}},
		}},
		Users: map[string]config.UserConfig{
			"alpha": {APIKeyHash: auth.HashAPIKey("alpha-key")},
			"beta":  {APIKeyHash: auth.HashAPIKey("beta-key")},
		},
		Pricing: config.PricingConfig{Models: map[string]config.ModelPricing{}},
	}
	authStore := auth.NewStore(cfg, nil)
	handler, err := NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	form := url.Values{}
	form.Set("action", "create")
	form.Set("model_name", "qwen2.5")
	form.Set("display_name", "Qwen 2.5")
	form.Set("backend_weights", "local:2, edge:1")
	form.Set("input_cost_per_1m_tokens", "0.45")
	form.Set("output_cost_per_1m_tokens", "0.9")
	form.Set("limit_access", "on")
	form.Add("user_access", "alpha")

	req := httptest.NewRequest(http.MethodPost, "/admin/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected model create page, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "qwen2.5") || !strings.Contains(body, "created.") {
		t.Fatalf("expected create success message, got %q", body)
	}

	entry, ok := handler.currentModelCatalog()["qwen2.5"]
	if !ok {
		t.Fatalf("expected new model in catalog")
	}
	if len(entry.Backends) != 2 || entry.Backends[0].Backend != "local" || entry.Backends[0].Weight != 2 {
		t.Fatalf("unexpected backend refs: %#v", entry.Backends)
	}

	price, ok := cfg.Pricing.Models["qwen2.5"]
	if !ok || price.InputCostPer1M != 0.45 || price.OutputCostPer1M != 0.9 {
		t.Fatalf("expected pricing update, got %#v", cfg.Pricing.Models["qwen2.5"])
	}

	alpha := cfg.Users["alpha"]
	beta := cfg.Users["beta"]
	if containsCSVValue(alpha.ModelDeny, "qwen2.5") {
		t.Fatalf("expected alpha to be allowed")
	}
	if !containsCSVValue(beta.ModelDeny, "qwen2.5") {
		t.Fatalf("expected beta to be denied")
	}
}

func TestModelUpdateAndDeleteWorkflow(t *testing.T) {
	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434"}, {Name: "edge", URL: "http://127.0.0.1:11435"}},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"qwen2.5": {Name: "qwen2.5", Backends: []config.ModelBackendRef{{Backend: "local", Weight: 1}}},
		}},
		Users: map[string]config.UserConfig{
			"alpha": {APIKeyHash: auth.HashAPIKey("alpha-key"), ModelAllow: []string{"qwen2.5"}},
			"beta":  {APIKeyHash: auth.HashAPIKey("beta-key"), ModelDeny: []string{"qwen2.5"}},
		},
		Pricing: config.PricingConfig{Models: map[string]config.ModelPricing{"qwen2.5": {InputCostPer1M: 0.45, OutputCostPer1M: 0.9}}},
	}
	authStore := auth.NewStore(cfg, nil)
	handler, err := NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	updateForm := url.Values{}
	updateForm.Set("action", "update")
	updateForm.Set("model_name", "qwen2.5")
	updateForm.Set("display_name", "Qwen 2.5 Turbo")
	updateForm.Set("backend_weights", "edge:3")
	updateForm.Set("input_cost_per_1m_tokens", "0.5")
	updateForm.Set("output_cost_per_1m_tokens", "1.1")

	updateReq := httptest.NewRequest(http.MethodPost, "/admin/models", strings.NewReader(updateForm.Encode()))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateReq.Header.Set("X-Admin-Token", "super-secret")
	updateResp := httptest.NewRecorder()
	handler.ServeHTTP(updateResp, updateReq)

	if updateResp.Code != http.StatusOK {
		t.Fatalf("expected model update page, got %d", updateResp.Code)
	}
	updated := handler.currentModelCatalog()["qwen2.5"]
	if updated.Name != "Qwen 2.5 Turbo" {
		t.Fatalf("expected updated display name, got %q", updated.Name)
	}
	if len(updated.Backends) != 1 || updated.Backends[0].Backend != "edge" || updated.Backends[0].Weight != 3 {
		t.Fatalf("expected updated backend refs, got %#v", updated.Backends)
	}
	if cfg.Pricing.Models["qwen2.5"].OutputCostPer1M != 1.1 {
		t.Fatalf("expected updated pricing, got %#v", cfg.Pricing.Models["qwen2.5"])
	}

	deleteForm := url.Values{}
	deleteForm.Set("action", "delete")
	deleteForm.Set("model_name", "qwen2.5")
	deleteReq := httptest.NewRequest(http.MethodPost, "/admin/models", strings.NewReader(deleteForm.Encode()))
	deleteReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deleteReq.Header.Set("X-Admin-Token", "super-secret")
	deleteResp := httptest.NewRecorder()
	handler.ServeHTTP(deleteResp, deleteReq)

	if deleteResp.Code != http.StatusOK {
		t.Fatalf("expected model delete page, got %d", deleteResp.Code)
	}
	if _, ok := handler.currentModelCatalog()["qwen2.5"]; ok {
		t.Fatalf("expected model removed from catalog")
	}
	if _, ok := cfg.Pricing.Models["qwen2.5"]; ok {
		t.Fatalf("expected model pricing removed")
	}
	if containsCSVValue(cfg.Users["alpha"].ModelAllow, "qwen2.5") {
		t.Fatalf("expected model removed from alpha allow list")
	}
	if containsCSVValue(cfg.Users["beta"].ModelDeny, "qwen2.5") {
		t.Fatalf("expected model removed from beta deny list")
	}
}

func TestModelMutationPersistsAndRefreshesRuntime(t *testing.T) {
	dir := t.TempDir()
	usageStore, err := usage.NewStore(filepath.Join(dir, "dashboard-model-persist.db"))
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	t.Cleanup(func() {
		_ = usageStore.Close()
	})

	cfg := &config.Config{
		Admin: config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{
			{Name: "local", URL: "http://127.0.0.1:11434", Weight: 1},
			{Name: "edge", URL: "http://127.0.0.1:11435", Weight: 1},
		},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local", Weight: 1}}},
		}},
		Users: map[string]config.UserConfig{
			"alpha": {APIKeyHash: auth.HashAPIKey("alpha-key")},
		},
		Pricing: config.PricingConfig{Models: map[string]config.ModelPricing{}},
	}

	authStore := auth.NewStore(cfg, usageStore.DB())
	if err := authStore.Validate(); err != nil {
		t.Fatalf("auth store validate: %v", err)
	}

	modelStore := models.NewStore(usageStore.DB())
	if _, err := modelStore.SyncDiscoveredCatalog(models.CatalogFromConfig(cfg)); err != nil {
		t.Fatalf("seed model catalog: %v", err)
	}
	activeCatalog, err := modelStore.LoadActiveCatalog()
	if err != nil {
		t.Fatalf("load seeded catalog: %v", err)
	}

	resolver, err := models.NewResolverWithCatalog(cfg, activeCatalog)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}

	handler, err := NewHandler(cfg, authStore, usageStore, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	handler.SetManager(resolver.Manager())
	handler.SetModelCatalog(activeCatalog)

	var capturedPricing *usage.PricingConfig
	handler.SetModelRuntimeRefreshers(
		resolver.RefreshCatalog,
		func(cfg *usage.PricingConfig) {
			capturedPricing = cfg
		},
	)

	form := url.Values{}
	form.Set("action", "create")
	form.Set("model_name", "qwen2.5")
	form.Set("display_name", "Qwen 2.5")
	form.Set("backend_weights", "edge:2")
	form.Set("input_cost_per_1m_tokens", "0.6")
	form.Set("output_cost_per_1m_tokens", "1.2")

	req := httptest.NewRequest(http.MethodPost, "/admin/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected model create page, got %d", resp.Code)
	}

	persistedCatalog, err := modelStore.LoadActiveCatalog()
	if err != nil {
		t.Fatalf("load persisted catalog: %v", err)
	}
	if _, ok := persistedCatalog["qwen2.5"]; !ok {
		t.Fatalf("expected qwen2.5 persisted in DB catalog")
	}

	pool, err := resolver.Resolve("qwen2.5", models.UserOverrides{})
	if err != nil {
		t.Fatalf("expected resolver refresh to expose qwen2.5, got %v", err)
	}
	backend, err := pool.Select()
	if err != nil {
		t.Fatalf("select refreshed backend: %v", err)
	}
	if backend == nil || backend.Name != "edge" {
		t.Fatalf("expected refreshed routing to use edge backend, got %#v", backend)
	}

	if capturedPricing == nil {
		t.Fatalf("expected pricing refresher callback to run")
	}
	if p, ok := capturedPricing.ModelPricing["qwen2.5"]; !ok || p.OutputCostPer1M != 1.2 {
		t.Fatalf("expected refreshed pricing for qwen2.5, got %#v", capturedPricing.ModelPricing["qwen2.5"])
	}
}

func TestBackendCreateUpdateRemovePersists(t *testing.T) {
	dir := t.TempDir()
	usageStore, err := usage.NewStore(filepath.Join(dir, "dashboard-backend-persist.db"))
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	t.Cleanup(func() {
		_ = usageStore.Close()
	})

	cfg := &config.Config{
		Admin: config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{
			Name:            "local",
			URL:             "http://127.0.0.1:11434",
			Weight:          1,
			Timeout:         30 * time.Second,
			HealthCheckPath: "/api/version",
		}},
		HealthCheck: config.HealthCheckConfig{IntervalSeconds: 10, TimeoutSeconds: 5, UnhealthyThreshold: 3},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"llama3.2": {Name: "llama3.2", Backends: []config.ModelBackendRef{{Backend: "local", Weight: 1}}},
		}},
		Users: map[string]config.UserConfig{"demo": {APIKeyHash: auth.HashAPIKey("demo-key")}},
	}

	authStore := auth.NewStore(cfg, usageStore.DB())
	if err := authStore.Validate(); err != nil {
		t.Fatalf("auth store validate: %v", err)
	}

	backendStore := backends.NewStore(usageStore.DB())
	if err := backendStore.SeedBackends(cfg.Backends); err != nil {
		t.Fatalf("seed backends: %v", err)
	}

	handler, err := NewHandler(cfg, authStore, usageStore, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	manager, err := backends.NewManager(cfg)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	handler.SetManager(manager)

	create := url.Values{}
	create.Set("action", "create")
	create.Set("backend_name", "edge")
	create.Set("backend_url", "http://127.0.0.1:11435")
	create.Set("backend_weight", "2")
	create.Set("backend_timeout_seconds", "45")
	create.Set("backend_health_path", "/health")
	create.Set("backend_tag", "gpu")

	req := httptest.NewRequest(http.MethodPost, "/admin/backends", strings.NewReader(create.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected backend create page, got %d", resp.Code)
	}

	loaded, err := backendStore.LoadActiveBackends()
	if err != nil {
		t.Fatalf("load active backends: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected two active backends after create, got %#v", loaded)
	}
	if _, ok := manager.GetByName("edge"); !ok {
		t.Fatalf("expected runtime manager to include new backend")
	}

	update := url.Values{}
	update.Set("action", "update")
	update.Set("backend_name", "edge")
	update.Set("backend_url", "http://127.0.0.1:12435")
	update.Set("backend_weight", "5")
	update.Set("backend_timeout_seconds", "90")
	update.Set("backend_health_path", "/alive")
	update.Set("backend_tag", "gpu-b")

	updateReq := httptest.NewRequest(http.MethodPost, "/admin/backends", strings.NewReader(update.Encode()))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateReq.Header.Set("X-Admin-Token", "super-secret")
	updateResp := httptest.NewRecorder()
	handler.ServeHTTP(updateResp, updateReq)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("expected backend update page, got %d", updateResp.Code)
	}

	edge, ok := manager.GetByName("edge")
	if !ok || edge.URL.String() != "http://127.0.0.1:12435" || edge.Weight != 5 || edge.Tag != "gpu-b" {
		t.Fatalf("expected runtime backend updated, got %#v", edge)
	}

	removeIntent := url.Values{}
	removeIntent.Set("action", "remove-intent")
	removeIntent.Set("backend_name", "edge")

	removeIntentReq := httptest.NewRequest(http.MethodPost, "/admin/backends", strings.NewReader(removeIntent.Encode()))
	removeIntentReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	removeIntentReq.Header.Set("X-Admin-Token", "super-secret")
	removeIntentResp := httptest.NewRecorder()
	handler.ServeHTTP(removeIntentResp, removeIntentReq)
	if removeIntentResp.Code != http.StatusOK {
		t.Fatalf("expected backend remove-intent page, got %d", removeIntentResp.Code)
	}
	if !strings.Contains(removeIntentResp.Body.String(), "Confirm removal") {
		t.Fatalf("expected remove confirmation prompt, got %q", removeIntentResp.Body.String())
	}

	removeConfirm := url.Values{}
	removeConfirm.Set("action", "remove-confirm")
	removeConfirm.Set("backend_name", "edge")
	removeConfirm.Set("confirm_backend_name", "edge")

	removeConfirmReq := httptest.NewRequest(http.MethodPost, "/admin/backends", strings.NewReader(removeConfirm.Encode()))
	removeConfirmReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	removeConfirmReq.Header.Set("X-Admin-Token", "super-secret")
	removeConfirmResp := httptest.NewRecorder()
	handler.ServeHTTP(removeConfirmResp, removeConfirmReq)
	if removeConfirmResp.Code != http.StatusOK {
		t.Fatalf("expected backend remove-confirm page, got %d", removeConfirmResp.Code)
	}

	loaded, err = backendStore.LoadActiveBackends()
	if err != nil {
		t.Fatalf("load backends after remove: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Name != "local" {
		t.Fatalf("expected only local active after remove, got %#v", loaded)
	}
	if _, ok := manager.GetByName("edge"); ok {
		t.Fatalf("expected runtime manager backend removed")
	}
}

func TestBackendRemoveBlockedWhenModelReferencesBackend(t *testing.T) {
	cfg := &config.Config{
		Admin: config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{
			Name:            "edge",
			URL:             "http://127.0.0.1:11435",
			Weight:          1,
			Timeout:         30 * time.Second,
			HealthCheckPath: "/api/version",
		}},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"qwen2.5": {Name: "qwen2.5", Backends: []config.ModelBackendRef{{Backend: "edge", Weight: 1}}},
		}},
		Users: map[string]config.UserConfig{"demo": {APIKeyHash: auth.HashAPIKey("demo-key")}},
	}
	authStore := auth.NewStore(cfg, nil)
	handler, err := NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	form := url.Values{}
	form.Set("action", "remove-intent")
	form.Set("backend_name", "edge")
	req := httptest.NewRequest(http.MethodPost, "/admin/backends", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected backend page response, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "Cannot remove backend") || !strings.Contains(body, "qwen2.5") {
		t.Fatalf("expected model-reference blocker message, got %q", body)
	}
	if _, ok := handler.backendByName("edge"); !ok {
		t.Fatalf("expected backend to remain configured when blocked")
	}
}

func TestBackendRemoveConfirmRequiresExactName(t *testing.T) {
	cfg := &config.Config{
		Admin: config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{
			Name:            "edge",
			URL:             "http://127.0.0.1:11435",
			Weight:          1,
			Timeout:         30 * time.Second,
			HealthCheckPath: "/api/version",
		}},
		Users: map[string]config.UserConfig{"demo": {APIKeyHash: auth.HashAPIKey("demo-key")}},
	}
	authStore := auth.NewStore(cfg, nil)
	handler, err := NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	form := url.Values{}
	form.Set("action", "remove-confirm")
	form.Set("backend_name", "edge")
	form.Set("confirm_backend_name", "EDGE")
	req := httptest.NewRequest(http.MethodPost, "/admin/backends", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected backend page response, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "Confirmation name does not match") {
		t.Fatalf("expected confirmation mismatch message, got %q", body)
	}
	if _, ok := handler.backendByName("edge"); !ok {
		t.Fatalf("expected backend to remain configured when confirmation fails")
	}
}

func TestThemeSelectorRendersAndDefaultsToBaseStyle(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected overview page, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "id=\"dashboard-theme-select\"") {
		t.Fatalf("expected theme selector in topbar, got %q", body)
	}
	if !strings.Contains(body, "<option value=\"default\" selected>Default</option>") {
		t.Fatalf("expected default theme option selected, got %q", body)
	}
	if !strings.Contains(body, "<option value=\"light\"") || !strings.Contains(body, "<option value=\"dark\"") || !strings.Contains(body, "<option value=\"matrix\"") || !strings.Contains(body, "<option value=\"space\"") {
		t.Fatalf("expected built-in theme options, got %q", body)
	}
	if strings.Contains(body, "href=\"/admin/static/themes/default.css\"") {
		t.Fatalf("did not expect a default override stylesheet, got %q", body)
	}
}

func TestThemeSelectionHTMXSetsCookieAndReturnsOOBSwap(t *testing.T) {
	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434"}},
		Users:    map[string]config.UserConfig{"demo": {APIKeyHash: auth.HashAPIKey("demo-key")}},
	}
	authStore := auth.NewStore(cfg, nil)
	handler, err := NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	form := url.Values{}
	form.Set("theme", "dark")
	req := httptest.NewRequest(http.MethodPost, "/admin/theme", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Current-URL", "http://localhost/admin/backends?view=compact")
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected HTMX theme response, got %d", resp.Code)
	}
	trigger := resp.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "dashboard-theme-updated") || !strings.Contains(trigger, "/admin/backends?view=compact") {
		t.Fatalf("expected HX-Trigger refresh metadata, got %q", trigger)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "id=\"dashboard-theme-css\"") || !strings.Contains(body, "hx-swap-oob=\"outerHTML\"") {
		t.Fatalf("expected out-of-band theme element swap, got %q", body)
	}
	if !strings.Contains(body, "href=\"/admin/static/themes/dark.css\"") {
		t.Fatalf("expected dark theme stylesheet link, got %q", body)
	}

	var themeCookie *http.Cookie
	for _, c := range resp.Result().Cookies() {
		if c.Name == "admin_theme" {
			themeCookie = c
			break
		}
	}
	if themeCookie == nil {
		t.Fatal("expected admin_theme cookie")
	}
	if themeCookie.Value != "dark" {
		t.Fatalf("expected dark cookie value, got %q", themeCookie.Value)
	}
	if themeCookie.Path != "/admin" {
		t.Fatalf("expected /admin cookie path, got %q", themeCookie.Path)
	}

	overviewReq := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	overviewReq.Header.Set("X-Admin-Token", "super-secret")
	overviewReq.AddCookie(themeCookie)
	overviewResp := httptest.NewRecorder()
	handler.ServeHTTP(overviewResp, overviewReq)

	if overviewResp.Code != http.StatusOK {
		t.Fatalf("expected themed overview render, got %d", overviewResp.Code)
	}
	rendered := overviewResp.Body.String()
	if !strings.Contains(rendered, "href=\"/admin/static/themes/dark.css\"") {
		t.Fatalf("expected selected dark theme stylesheet in rendered page, got %q", rendered)
	}
	if !strings.Contains(rendered, "<option value=\"dark\" selected>Dark</option>") {
		t.Fatalf("expected dark option selected, got %q", rendered)
	}
}

func TestThemeSelectionInvalidFallsBackToDefault(t *testing.T) {
	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")},
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434"}},
		Users:    map[string]config.UserConfig{"demo": {APIKeyHash: auth.HashAPIKey("demo-key")}},
	}
	authStore := auth.NewStore(cfg, nil)
	handler, err := NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	form := url.Values{}
	form.Set("theme", "../../etc/passwd")
	req := httptest.NewRequest(http.MethodPost, "/admin/theme", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("X-Admin-Token", "super-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected HTMX fallback response, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "<style id=\"dashboard-theme-css\"") {
		t.Fatalf("expected default style placeholder for invalid theme, got %q", body)
	}
	for _, c := range resp.Result().Cookies() {
		if c.Name == "admin_theme" && c.Value != "default" {
			t.Fatalf("expected invalid theme to fallback to default cookie, got %q", c.Value)
		}
	}
}

func TestServeStaticThemeFileHasCSSContentType(t *testing.T) {
	cfg := &config.Config{Admin: config.AdminConfig{TokenHash: auth.HashAPIKey("super-secret")}}
	authStore := auth.NewStore(cfg, nil)
	handler, err := NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/static/themes/dark.css", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected static theme file, got %d", resp.Code)
	}
	if got := resp.Header().Get("Content-Type"); !strings.Contains(got, "text/css") {
		t.Fatalf("expected css content type, got %q", got)
	}
	if !strings.Contains(resp.Body.String(), ":root") {
		t.Fatalf("expected css file content, got %q", resp.Body.String())
	}
}
