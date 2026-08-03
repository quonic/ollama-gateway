package dashboard

import (
	"embed"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/backends"
	"ollama-gateway/internal/config"
	"ollama-gateway/internal/usage"
)

//go:embed templates/*.html static/*
var dashboardFS embed.FS

type Handler struct {
	cfg        *config.Config
	authStore  *auth.Store
	usageStore *usage.Store
	templates  *template.Template
	state      *state
	manager    *backends.Manager
	models     map[string]config.ModelEntry
}

type state struct {
	disabledBackends map[string]bool
}

func NewHandler(cfg *config.Config, authStore *auth.Store, usageStore *usage.Store, templates *template.Template) (*Handler, error) {
	h := &Handler{
		cfg:        cfg,
		authStore:  authStore,
		usageStore: usageStore,
		state: &state{
			disabledBackends: make(map[string]bool),
		},
	}
	if manager, err := backends.NewManager(cfg); err == nil {
		h.manager = manager
	}
	if templates == nil {
		t, err := template.New("dashboard").Funcs(template.FuncMap{
			"formatCost":       formatCost,
			"humanizeDuration": humanizeDuration,
			"trim":             trimText,
			"add":              addInt,
			"sub":              subInt,
		}).ParseFS(dashboardFS, "templates/*.html")
		if err != nil {
			return nil, fmt.Errorf("parse dashboard templates: %w", err)
		}
		h.templates = t
	} else {
		h.templates = templates
	}
	return h, nil
}

func (h *Handler) SetManager(manager *backends.Manager) {
	h.manager = manager
}

func (h *Handler) SetModelCatalog(catalog map[string]config.ModelEntry) {
	if catalog == nil {
		h.models = nil
		return
	}
	cloned := make(map[string]config.ModelEntry, len(catalog))
	for name, entry := range catalog {
		refs := make([]config.ModelBackendRef, 0, len(entry.Backends))
		for _, ref := range entry.Backends {
			refs = append(refs, config.ModelBackendRef{Backend: ref.Backend, Weight: ref.Weight})
		}
		cloned[name] = config.ModelEntry{Name: entry.Name, Backends: refs}
	}
	h.models = cloned
}

func (h *Handler) currentModelCatalog() map[string]config.ModelEntry {
	if h.models != nil {
		return h.models
	}
	return h.cfg.Models.Models
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin")
	if path == "" || path == "/" {
		path = "/overview"
	}
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		path = "overview"
	}

	if strings.HasPrefix(path, "static/") {
		h.serveStatic(w, path)
		return
	}

	if r.Method == http.MethodPost && path == "login" {
		h.handleLogin(w, r)
		return
	}
	if r.Method == http.MethodPost && path == "users" {
		h.handleUserAction(w, r)
		return
	}
	if path == "login" {
		h.renderLogin(w, r, false)
		return
	}

	if !h.isAuthenticated(r) {
		h.renderLogin(w, r, true)
		return
	}

	switch path {
	case "overview":
		h.renderOverview(w, r)
	case "models":
		h.renderModels(w, r)
	case "backends":
		h.renderBackends(w, r)
	case "users":
		h.renderUsers(w, r)
	case "logs":
		h.renderLogs(w, r)
	case "backends/health":
		h.renderBackendHealth(w, r)
	default:
		if strings.HasPrefix(path, "backends/toggle/") {
			h.handleBackendToggle(w, path)
			return
		}
		h.renderOverview(w, r)
	}
}

func (h *Handler) isAuthenticated(r *http.Request) bool {
	if token := r.Header.Get("X-Admin-Token"); token != "" && h.authStore.CheckAdminToken(token) {
		return true
	}
	cookie, err := r.Cookie("admin_session")
	if err != nil {
		return false
	}
	return cookie.Value != "" && h.authStore.CheckAdminToken(cookie.Value)
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.httpError(w, http.StatusBadRequest, "invalid form")
		return
	}
	provided := r.FormValue("token")
	if !h.authStore.CheckAdminToken(provided) {
		h.renderLogin(w, r, true)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    provided,
		Path:     "/",
		HttpOnly: true,
	})
	http.Redirect(w, r, "/admin/overview", http.StatusSeeOther)
}

func (h *Handler) renderLogin(w http.ResponseWriter, r *http.Request, invalid bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if invalid {
		w.WriteHeader(http.StatusForbidden)
	}
	if err := h.templates.ExecuteTemplate(w, "login.html", map[string]any{"Title": "Admin Login", "Invalid": invalid}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderOverview(w http.ResponseWriter, r *http.Request) {
	models := h.currentModelCatalog()
	data := map[string]any{
		"Title":            "Overview",
		"Subtitle":         "Live snapshot of gateway capacity, spend, and backend health.",
		"Active":           "overview",
		"ContentBlock":     "content-overview",
		"BackendCount":     len(h.cfg.Backends),
		"ModelCount":       len(models),
		"UserCount":        len(h.cfg.Users),
		"Backends":         h.cfg.Backends,
		"DisabledBackends": h.state.disabledBackends,
	}
	if h.usageStore != nil {
		summary, err := h.loadOverviewSummary()
		if err == nil {
			data["Summary"] = summary
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "overview.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderModels(w http.ResponseWriter, r *http.Request) {
	models := h.currentModelCatalog()
	data := map[string]any{
		"Title":            "Models",
		"Subtitle":         "Routing map from model aliases to backend targets.",
		"Active":           "models",
		"ContentBlock":     "content-models",
		"Models":           models,
		"Backends":         h.cfg.Backends,
		"DisabledBackends": h.state.disabledBackends,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "models.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderBackends(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Title":            "Backends",
		"Subtitle":         "Enable or disable backend pools without restarting the service.",
		"Active":           "backends",
		"ContentBlock":     "content-backends",
		"Backends":         h.cfg.Backends,
		"DisabledBackends": h.state.disabledBackends,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "backends.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderUsers(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Title":            "Users",
		"Subtitle":         "Generate API keys and audit configured user identities.",
		"Active":           "users",
		"ContentBlock":     "content-users",
		"Users":            h.cfg.Users,
		"DisabledBackends": h.state.disabledBackends,
		"GeneratedKey":     "",
		"GeneratedHash":    "",
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "users.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderLogs(w http.ResponseWriter, r *http.Request) {
	page := 1
	if pageParam := r.URL.Query().Get("page"); pageParam != "" {
		if parsed, err := strconv.Atoi(pageParam); err == nil && parsed > 0 {
			page = parsed
		}
	}
	filters := usage.ListOptions{
		APIKeyID: r.URL.Query().Get("api_key_id"),
		Model:    r.URL.Query().Get("model"),
		Start:    r.URL.Query().Get("start"),
		End:      r.URL.Query().Get("end"),
		Page:     page,
		PageSize: 10,
	}
	data := map[string]any{
		"Title":            "Logs",
		"Subtitle":         "Filter request history and inspect model-level usage trends.",
		"Active":           "logs",
		"ContentBlock":     "content-logs",
		"DisabledBackends": h.state.disabledBackends,
		"Filters":          filters,
		"Page":             page,
	}
	if h.usageStore != nil {
		rows, err := h.loadRecentRecords(filters)
		if err == nil {
			data["Records"] = rows
		}
		analytics, err := h.loadLogsAnalytics(filters)
		if err == nil {
			data["Analytics"] = analytics
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "logs.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderBackendHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, b := range h.cfg.Backends {
		if h.state.disabledBackends[b.Name] {
			fmt.Fprintf(w, "%s:disabled\n", b.Name)
			continue
		}
		fmt.Fprintf(w, "%s:healthy\n", b.Name)
	}
}

func (h *Handler) handleBackendToggle(w http.ResponseWriter, path string) {
	name := strings.TrimPrefix(path, "backends/toggle/")
	if name == "" || name == "backends/toggle/" {
		h.httpError(w, http.StatusBadRequest, "backend name required")
		return
	}
	if h.manager != nil {
		if b, ok := h.manager.GetByName(name); ok {
			if h.state.disabledBackends[name] {
				delete(h.state.disabledBackends, name)
				b.SetEnabled(true)
				fmt.Fprintf(w, "Backend %q enabled", name)
				return
			}
			h.state.disabledBackends[name] = true
			b.SetEnabled(false)
			fmt.Fprintf(w, "Backend %q disabled", name)
			return
		}
	}
	if h.state.disabledBackends[name] {
		delete(h.state.disabledBackends, name)
		fmt.Fprintf(w, "Backend %q enabled", name)
		return
	}
	h.state.disabledBackends[name] = true
	fmt.Fprintf(w, "Backend %q disabled", name)
}

func (h *Handler) handleUserAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.httpError(w, http.StatusBadRequest, "invalid form")
		return
	}
	if r.FormValue("action") != "generate" {
		h.renderUsers(w, r)
		return
	}

	rawKey := generateAPIKey()
	hash := auth.HashAPIKey(rawKey)
	data := map[string]any{
		"Title":            "Users",
		"Subtitle":         "Generate API keys and audit configured user identities.",
		"Active":           "users",
		"ContentBlock":     "content-users",
		"Users":            h.cfg.Users,
		"DisabledBackends": h.state.disabledBackends,
		"GeneratedKey":     rawKey,
		"GeneratedHash":    hash,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "users.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) serveStatic(w http.ResponseWriter, path string) {
	assetPath := "static/" + strings.TrimPrefix(path, "static/")
	data, err := dashboardFS.ReadFile(assetPath)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	ext := strings.ToLower(filepath.Ext(assetPath))
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		switch ext {
		case ".css":
			contentType = "text/css; charset=utf-8"
		case ".js":
			contentType = "application/javascript"
		}
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}

func (h *Handler) httpError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprint(w, msg)
}

func (h *Handler) loadRecentRecords(opts usage.ListOptions) ([]usage.UsageRecord, error) {
	if h.usageStore == nil {
		return nil, fmt.Errorf("usage store not configured")
	}
	return h.usageStore.ListRecords(opts)
}

func (h *Handler) loadLogsAnalytics(opts usage.ListOptions) (usage.LogsAnalytics, error) {
	if h.usageStore == nil {
		return usage.LogsAnalytics{}, fmt.Errorf("usage store not configured")
	}
	return h.usageStore.LogsAnalytics(opts)
}

func (h *Handler) loadOverviewSummary() (map[string]any, error) {
	if h.usageStore == nil {
		return nil, fmt.Errorf("usage store not configured")
	}
	summary, err := h.usageStore.OverviewSummary()
	if err != nil {
		return nil, err
	}
	breakdown, err := h.usageStore.ModelCostBreakdown(10)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"requests":         summary.Requests,
		"promptTokens":     summary.PromptTokens,
		"completionTokens": summary.CompletionTokens,
		"cost":             summary.Cost,
		"modelBreakdown":   breakdown,
	}, nil
}

func generateAPIKey() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 32)
	for i := range b {
		b[i] = alphabet[i%len(alphabet)]
	}
	return string(b)
}

func formatCost(v any) string {
	if v == nil {
		return "$0.00"
	}
	switch val := v.(type) {
	case float64:
		return fmt.Sprintf("$%.2f", val)
	case float32:
		return fmt.Sprintf("$%.2f", val)
	case int:
		return fmt.Sprintf("$%.2f", float64(val))
	case int64:
		return fmt.Sprintf("$%.2f", float64(val))
	default:
		return "$0.00"
	}
}

func humanizeDuration(ms int) string {
	if ms <= 0 {
		return "0ms"
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	seconds := float64(ms) / 1000
	return fmt.Sprintf("%.1fs", seconds)
}

func trimText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func addInt(a, b int) int {
	return a + b
}

func subInt(a, b int) int {
	return a - b
}
