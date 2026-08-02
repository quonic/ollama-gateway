package dashboard

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"ollama-gateway/internal/auth"
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
	if templates == nil {
		t, err := template.New("dashboard").Funcs(template.FuncMap{
			"formatCost":       formatCost,
			"humanizeDuration": humanizeDuration,
			"trim":             trimText,
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
	data := map[string]any{
		"Title":            "Overview",
		"Active":           "overview",
		"BackendCount":     len(h.cfg.Backends),
		"ModelCount":       len(h.cfg.Models.Models),
		"UserCount":        len(h.cfg.Users),
		"Backends":         h.cfg.Backends,
		"DisabledBackends": h.state.disabledBackends,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "overview.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderModels(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Title":            "Models",
		"Active":           "models",
		"Models":           h.cfg.Models.Models,
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
		"Active":           "backends",
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
		"Active":           "users",
		"Users":            h.cfg.Users,
		"DisabledBackends": h.state.disabledBackends,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "users.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderLogs(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Title":            "Logs",
		"Active":           "logs",
		"DisabledBackends": h.state.disabledBackends,
	}
	if h.usageStore != nil {
		rows, err := h.loadRecentRecords(10)
		if err == nil {
			data["Records"] = rows
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
	if h.state.disabledBackends[name] {
		delete(h.state.disabledBackends, name)
		fmt.Fprintf(w, "Backend %q enabled", name)
		return
	}
	h.state.disabledBackends[name] = true
	fmt.Fprintf(w, "Backend %q disabled", name)
}

func (h *Handler) serveStatic(w http.ResponseWriter, path string) {
	data, err := dashboardFS.ReadFile("static/" + strings.TrimPrefix(path, "static/"))
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(data))
	_, _ = w.Write(data)
}

func (h *Handler) httpError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprint(w, msg)
}

func (h *Handler) loadRecentRecords(limit int) ([]usage.UsageRecord, error) {
	if h.usageStore == nil {
		return nil, fmt.Errorf("usage store not configured")
	}
	rows, err := h.usageStore.DB().Query(`SELECT id, timestamp, api_key_id, model, backend_url, prompt_tokens, completion_tokens, duration_ms, cost_usd FROM usage_records ORDER BY timestamp DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []usage.UsageRecord
	for rows.Next() {
		var rec usage.UsageRecord
		if err := rows.Scan(&rec.ID, &rec.Timestamp, &rec.APIKeyID, &rec.Model, &rec.BackendURL, &rec.PromptTokens, &rec.CompletionTokens, &rec.DurationMS, &rec.CostUSD); err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
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
