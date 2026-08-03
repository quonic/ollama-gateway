package dashboard

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/backends"
	"ollama-gateway/internal/config"
	"ollama-gateway/internal/models"
	"ollama-gateway/internal/tlsruntime"
	"ollama-gateway/internal/usage"
)

//go:embed templates/*.html static/*
var dashboardFS embed.FS

type Handler struct {
	cfg          *config.Config
	authStore    *auth.Store
	usageStore   *usage.Store
	templates    *template.Template
	state        *state
	manager      *backends.Manager
	tlsManager   *tlsruntime.Manager
	models       map[string]config.ModelEntry
	modelStore   *models.Store
	backendStore *backends.Store

	reloadStatusProvider func() config.ReloadStatus

	refreshResolverCatalog func(map[string]config.ModelEntry)
	refreshProxyPricing    func(*usage.PricingConfig)
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
	if usageStore != nil {
		h.modelStore = models.NewStore(usageStore.DB())
		h.backendStore = backends.NewStore(usageStore.DB())
	}
	if templates == nil {
		t, err := template.New("dashboard").Funcs(template.FuncMap{
			"formatCost":       formatCost,
			"humanizeDuration": humanizeDuration,
			"trim":             trimText,
			"add":              addInt,
			"sub":              subInt,
			"contains":         containsString,
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

func (h *Handler) SetTLSManager(manager *tlsruntime.Manager) {
	h.tlsManager = manager
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

func (h *Handler) SetModelRuntimeRefreshers(
	refreshResolverCatalog func(map[string]config.ModelEntry),
	refreshProxyPricing func(*usage.PricingConfig),
) {
	h.refreshResolverCatalog = refreshResolverCatalog
	h.refreshProxyPricing = refreshProxyPricing
}

func (h *Handler) SetReloadStatusProvider(provider func() config.ReloadStatus) {
	h.reloadStatusProvider = provider
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
	if r.Method == http.MethodPost && path == "models" {
		h.handleModelAction(w, r)
		return
	}
	if r.Method == http.MethodPost && path == "backends" {
		h.handleBackendAction(w, r)
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
			h.handleBackendToggle(w, r, path)
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
	users := h.usersSnapshot()
	tlsEnabled := strings.TrimSpace(h.cfg.Server.TLSCertPath) != "" && strings.TrimSpace(h.cfg.Server.TLSKeyPath) != ""
	data := map[string]any{
		"Title":            "Overview",
		"Subtitle":         "Live snapshot of gateway capacity, spend, and backend health.",
		"Active":           "overview",
		"ContentBlock":     "content-overview",
		"BackendCount":     len(h.cfg.Backends),
		"ModelCount":       len(models),
		"UserCount":        len(users),
		"Backends":         h.cfg.Backends,
		"DisabledBackends": h.state.disabledBackends,
	}
	if tlsEnabled {
		tlsStatus := map[string]any{
			"Enabled":       true,
			"CertPath":      h.cfg.Server.TLSCertPath,
			"CheckInterval": h.cfg.Server.TLSCheckInterval.String(),
			"StatusLabel":   "unavailable",
			"StatusClass":   "disabled",
		}
		if h.tlsManager != nil {
			status := h.tlsManager.Status()
			tlsStatus["Loaded"] = status.Loaded
			tlsStatus["CheckInterval"] = status.CheckInterval.String()
			if status.Loaded {
				tlsStatus["ExpiresAt"] = status.ExpiresAt.Format(time.RFC3339)
				tlsStatus["LastReloadAt"] = status.LastReloadAt.Format(time.RFC3339)
				tlsStatus["DaysRemaining"] = status.DaysRemaining
				tlsStatus["StatusLabel"] = "healthy"
				tlsStatus["StatusClass"] = "healthy"
				if status.ExpiringSoon {
					tlsStatus["StatusLabel"] = "expiring"
					tlsStatus["StatusClass"] = "warning"
				}
				if status.Expired {
					tlsStatus["StatusLabel"] = "expired"
					tlsStatus["StatusClass"] = "expired"
				}
			}
		}
		data["TLSStatus"] = tlsStatus
	}
	if h.usageStore != nil {
		summary, err := h.loadOverviewSummary()
		if err == nil {
			data["Summary"] = summary
		}
	}
	if h.reloadStatusProvider != nil {
		reloadStatus := h.reloadStatusProvider()
		statusClass := "healthy"
		statusLabel := "ready"
		if reloadStatus.LastError != "" {
			statusClass = "warning"
			statusLabel = "error"
		}
		if reloadStatus.LastTrigger == "" && reloadStatus.LastReloadAt.IsZero() {
			statusClass = "disabled"
			statusLabel = "not-run"
		}

		data["ConfigReloadStatus"] = map[string]any{
			"StatusClass":  statusClass,
			"StatusLabel":  statusLabel,
			"LastTrigger":  fallbackValue(reloadStatus.LastTrigger, "n/a"),
			"LastReloadAt": formatOptionalTimestamp(reloadStatus.LastReloadAt),
			"LastError":    fallbackValue(reloadStatus.LastError, "none"),
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "overview.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func fallbackValue(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func formatOptionalTimestamp(ts time.Time) string {
	if ts.IsZero() {
		return "never"
	}
	return ts.Format(time.RFC3339)
}

func (h *Handler) renderModels(w http.ResponseWriter, r *http.Request) {
	h.renderModelsPage(w, "", "")
}

func (h *Handler) renderModelsPage(w http.ResponseWriter, formError, formSuccess string) {
	models := h.currentModelCatalog()
	users := h.usersSnapshot()
	allUsers := make([]string, 0, len(users))
	for userID := range users {
		allUsers = append(allUsers, userID)
	}
	sort.Strings(allUsers)

	type modelView struct {
		Name               string
		DisplayName        string
		BackendWeightsText string
		InputCost          float64
		OutputCost         float64
		AccessUsers        []string
		AccessUsersText    string
		AccessLimited      bool
	}

	viewModels := make([]modelView, 0, len(models))
	modelNames := make([]string, 0, len(models))
	for modelName := range models {
		modelNames = append(modelNames, modelName)
	}
	sort.Strings(modelNames)

	for _, modelName := range modelNames {
		entry := models[modelName]
		allowedUsers := make([]string, 0, len(allUsers))
		for _, userID := range allUsers {
			if modelAccessibleForUser(users[userID], modelName) {
				allowedUsers = append(allowedUsers, userID)
			}
		}
		inputCost := 0.0
		outputCost := 0.0
		if mp, ok := h.cfg.Pricing.Models[modelName]; ok {
			inputCost = mp.InputCostPer1M
			outputCost = mp.OutputCostPer1M
		}

		viewModels = append(viewModels, modelView{
			Name:               modelName,
			DisplayName:        entry.Name,
			BackendWeightsText: formatModelBackendRefs(entry.Backends),
			InputCost:          inputCost,
			OutputCost:         outputCost,
			AccessUsers:        allowedUsers,
			AccessUsersText:    strings.Join(allowedUsers, ", "),
			AccessLimited:      len(allUsers) > 0 && len(allowedUsers) != len(allUsers),
		})
	}

	data := map[string]any{
		"Title":             "Models",
		"Subtitle":          "Create and edit model routing, pricing, and user access policies.",
		"Active":            "models",
		"ContentBlock":      "content-models",
		"Models":            viewModels,
		"Backends":          h.cfg.Backends,
		"AllUsers":          allUsers,
		"DisabledBackends":  h.state.disabledBackends,
		"DefaultInputCost":  h.cfg.Pricing.DefaultInputPer1M,
		"DefaultOutputCost": h.cfg.Pricing.DefaultOutputPer1M,
		"FormError":         formError,
		"FormSuccess":       formSuccess,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "models.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderBackends(w http.ResponseWriter, r *http.Request) {
	h.renderBackendsPage(w, "", "", "")
}

func (h *Handler) renderBackendsPage(w http.ResponseWriter, formError, formSuccess, pendingRemove string) {
	viewBackends := make([]config.Backend, len(h.cfg.Backends))
	copy(viewBackends, h.cfg.Backends)
	sort.Slice(viewBackends, func(i, j int) bool {
		return viewBackends[i].Name < viewBackends[j].Name
	})

	data := map[string]any{
		"Title":            "Backends",
		"Subtitle":         "Create, edit, and remove backend targets without restarting the service.",
		"Active":           "backends",
		"ContentBlock":     "content-backends",
		"Backends":         viewBackends,
		"DisabledBackends": h.state.disabledBackends,
		"FormError":        formError,
		"FormSuccess":      formSuccess,
		"PendingRemove":    pendingRemove,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "backends.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderUsers(w http.ResponseWriter, r *http.Request) {
	h.renderUsersPage(w, "", "", "", "")
}

func (h *Handler) renderUsersPage(w http.ResponseWriter, generatedKey, generatedHash, formError, formSuccess string) {
	users := h.usersSnapshot()
	viewUsers := make([]map[string]any, 0, len(users))
	for id, uc := range users {
		stats, statsErr := h.loadUserStats(id)
		viewUsers = append(viewUsers, map[string]any{
			"ID":         id,
			"Config":     uc,
			"AllowText":  strings.Join(uc.ModelAllow, ", "),
			"DenyText":   strings.Join(uc.ModelDeny, ", "),
			"AliasText":  formatAliases(uc.Aliases),
			"Rate":       userRateOrDefault(uc.RateLimit, h.cfg.RateLimit.DefaultRate),
			"Burst":      userBurstOrDefault(uc.RateLimit, h.cfg.RateLimit.DefaultBurst),
			"TTLSeconds": userTTLOrDefaultSeconds(uc.RateLimit, h.cfg.RateLimit.TTL),
			"Stats":      stats,
			"HasStats":   statsErr == nil,
		})
	}
	sort.Slice(viewUsers, func(i, j int) bool {
		return viewUsers[i]["ID"].(string) < viewUsers[j]["ID"].(string)
	})

	data := map[string]any{
		"Title":            "Users",
		"Subtitle":         "Create users, generate API keys, and audit active identities.",
		"Active":           "users",
		"ContentBlock":     "content-users",
		"Users":            viewUsers,
		"DisabledBackends": h.state.disabledBackends,
		"GeneratedKey":     generatedKey,
		"GeneratedHash":    generatedHash,
		"FormError":        formError,
		"FormSuccess":      formSuccess,
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

func (h *Handler) handleBackendToggle(w http.ResponseWriter, r *http.Request, path string) {
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
				h.respondBackendToggle(w, r, name, true)
				return
			}
			h.state.disabledBackends[name] = true
			b.SetEnabled(false)
			h.respondBackendToggle(w, r, name, false)
			return
		}
	}
	if h.state.disabledBackends[name] {
		delete(h.state.disabledBackends, name)
		h.respondBackendToggle(w, r, name, true)
		return
	}
	h.state.disabledBackends[name] = true
	h.respondBackendToggle(w, r, name, false)
}

func (h *Handler) respondBackendToggle(w http.ResponseWriter, r *http.Request, name string, enabled bool) {
	status := "disabled"
	if enabled {
		status = "enabled"
	}
	if r != nil && r.Method == http.MethodPost {
		http.Redirect(w, r, "/admin/backends", http.StatusSeeOther)
		return
	}
	fmt.Fprintf(w, "Backend %q %s", name, status)
}

func (h *Handler) handleBackendAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.httpError(w, http.StatusBadRequest, "invalid form")
		return
	}

	action := strings.TrimSpace(r.FormValue("action"))
	name := strings.TrimSpace(r.FormValue("backend_name"))
	if name == "" {
		h.renderBackendsPage(w, "Backend name is required.", "", "")
		return
	}

	if action == "remove-intent" {
		if _, exists := h.backendByName(name); !exists {
			h.renderBackendsPage(w, fmt.Sprintf("Backend %q not found.", name), "", "")
			return
		}
		blockingModels := h.modelsReferencingBackend(name)
		if len(blockingModels) > 0 {
			h.renderBackendsPage(w, fmt.Sprintf("Cannot remove backend %q. Reassign these models first: %s", name, strings.Join(blockingModels, ", ")), "", "")
			return
		}
		h.renderBackendsPage(w, "", fmt.Sprintf("Confirm removal for backend %q.", name), name)
		return
	}

	if action == "remove-confirm" {
		if strings.TrimSpace(r.FormValue("confirm_backend_name")) != name {
			h.renderBackendsPage(w, "Confirmation name does not match backend name.", "", name)
			return
		}
		blockingModels := h.modelsReferencingBackend(name)
		if len(blockingModels) > 0 {
			h.renderBackendsPage(w, fmt.Sprintf("Cannot remove backend %q. Reassign these models first: %s", name, strings.Join(blockingModels, ", ")), "", "")
			return
		}
		if err := h.removeBackend(name); err != nil {
			h.renderBackendsPage(w, fmt.Sprintf("Failed to remove backend %q: %v", name, err), "", "")
			return
		}
		h.renderBackendsPage(w, "", fmt.Sprintf("Backend %q removed.", name), "")
		return
	}

	backendCfg, err := h.buildBackendConfigFromForm(r)
	if err != nil {
		h.renderBackendsPage(w, fmt.Sprintf("Invalid backend settings for %q: %v", name, err), "", "")
		return
	}

	switch action {
	case "create":
		if err := h.createBackend(backendCfg); err != nil {
			h.renderBackendsPage(w, fmt.Sprintf("Failed to create backend %q: %v", name, err), "", "")
			return
		}
		h.renderBackendsPage(w, "", fmt.Sprintf("Backend %q created.", name), "")
	case "update":
		if err := h.updateBackend(backendCfg); err != nil {
			h.renderBackendsPage(w, fmt.Sprintf("Failed to update backend %q: %v", name, err), "", "")
			return
		}
		h.renderBackendsPage(w, "", fmt.Sprintf("Backend %q updated.", name), "")
	default:
		h.renderBackendsPage(w, "Unknown backend action.", "", "")
	}
}

func (h *Handler) buildBackendConfigFromForm(r *http.Request) (config.Backend, error) {
	name := strings.TrimSpace(r.FormValue("backend_name"))
	url := strings.TrimSpace(r.FormValue("backend_url"))
	if name == "" {
		return config.Backend{}, fmt.Errorf("backend name is required")
	}
	if url == "" {
		return config.Backend{}, fmt.Errorf("backend url is required")
	}

	weight, err := strconv.Atoi(strings.TrimSpace(r.FormValue("backend_weight")))
	if err != nil || weight <= 0 {
		return config.Backend{}, fmt.Errorf("weight must be a positive integer")
	}

	timeoutSeconds, err := strconv.Atoi(strings.TrimSpace(r.FormValue("backend_timeout_seconds")))
	if err != nil || timeoutSeconds <= 0 {
		return config.Backend{}, fmt.Errorf("timeout seconds must be a positive integer")
	}

	healthPath := strings.TrimSpace(r.FormValue("backend_health_path"))
	if healthPath == "" {
		healthPath = "/api/version"
	}
	if !strings.HasPrefix(healthPath, "/") {
		return config.Backend{}, fmt.Errorf("health check path must start with /")
	}

	return config.Backend{
		Name:            name,
		URL:             url,
		Weight:          weight,
		Tag:             strings.TrimSpace(r.FormValue("backend_tag")),
		Timeout:         time.Duration(timeoutSeconds) * time.Second,
		HealthCheckPath: healthPath,
	}, nil
}

func (h *Handler) createBackend(backendCfg config.Backend) error {
	if _, exists := h.backendByName(backendCfg.Name); exists {
		return fmt.Errorf("backend already exists")
	}
	if h.backendStore != nil {
		if err := h.backendStore.UpsertBackend(backendCfg); err != nil {
			return err
		}
	}
	if h.manager != nil {
		if err := h.manager.UpsertBackend(backendCfg); err != nil {
			return err
		}
	}
	delete(h.state.disabledBackends, backendCfg.Name)
	h.upsertBackendInConfig(backendCfg)
	return h.reloadBackendsFromStoreIfAvailable()
}

func (h *Handler) updateBackend(backendCfg config.Backend) error {
	if _, exists := h.backendByName(backendCfg.Name); !exists {
		return fmt.Errorf("backend not found")
	}
	if h.backendStore != nil {
		if err := h.backendStore.UpsertBackend(backendCfg); err != nil {
			return err
		}
	}
	if h.manager != nil {
		if err := h.manager.UpsertBackend(backendCfg); err != nil {
			return err
		}
	}
	delete(h.state.disabledBackends, backendCfg.Name)
	h.upsertBackendInConfig(backendCfg)
	return h.reloadBackendsFromStoreIfAvailable()
}

func (h *Handler) removeBackend(name string) error {
	if h.backendStore != nil {
		if err := h.backendStore.RemoveBackend(name); err != nil {
			if errors.Is(err, backends.ErrBackendNotFound) {
				return fmt.Errorf("backend not found")
			}
			return err
		}
	}
	if h.manager != nil {
		if err := h.manager.RemoveBackend(name); err != nil {
			if errors.Is(err, backends.ErrBackendNotFound) {
				return fmt.Errorf("backend not found")
			}
			return err
		}
	}
	delete(h.state.disabledBackends, name)
	h.removeBackendFromConfig(name)
	return h.reloadBackendsFromStoreIfAvailable()
}

func (h *Handler) modelsReferencingBackend(backendName string) []string {
	catalog := h.currentModelCatalog()
	out := []string{}
	for modelName, entry := range catalog {
		for _, ref := range entry.Backends {
			if ref.Backend == backendName {
				out = append(out, modelName)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func (h *Handler) backendByName(name string) (config.Backend, bool) {
	for _, b := range h.cfg.Backends {
		if b.Name == name {
			return b, true
		}
	}
	return config.Backend{}, false
}

func (h *Handler) upsertBackendInConfig(updated config.Backend) {
	for i, b := range h.cfg.Backends {
		if b.Name == updated.Name {
			h.cfg.Backends[i] = updated
			return
		}
	}
	h.cfg.Backends = append(h.cfg.Backends, updated)
}

func (h *Handler) removeBackendFromConfig(name string) {
	filtered := make([]config.Backend, 0, len(h.cfg.Backends))
	for _, b := range h.cfg.Backends {
		if b.Name == name {
			continue
		}
		filtered = append(filtered, b)
	}
	h.cfg.Backends = filtered
}

func (h *Handler) reloadBackendsFromStoreIfAvailable() error {
	if h.backendStore == nil {
		return nil
	}
	activeBackends, err := h.backendStore.LoadActiveBackends()
	if err != nil {
		return err
	}
	h.cfg.Backends = activeBackends
	activeNames := map[string]bool{}
	for _, b := range activeBackends {
		activeNames[b.Name] = true
	}
	for name := range h.state.disabledBackends {
		if !activeNames[name] {
			delete(h.state.disabledBackends, name)
		}
	}
	return nil
}

func (h *Handler) handleUserAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.httpError(w, http.StatusBadRequest, "invalid form")
		return
	}
	action := r.FormValue("action")
	if action == "generate" {
		rawKey := generateAPIKey()
		hash := auth.HashAPIKey(rawKey)
		h.renderUsersPage(w, rawKey, hash, "", "")
		return
	}

	if action == "rotate" {
		userName := strings.TrimSpace(r.FormValue("user_name"))
		if userName == "" {
			h.renderUsersPage(w, "", "", "User name is required for key rotation.", "")
			return
		}
		rawKey := generateAPIKey()
		rotatedRaw, rotatedHash, err := h.authStore.RotateUserKey(userName, rawKey)
		if err != nil {
			if errors.Is(err, auth.ErrUserNotFound) {
				h.renderUsersPage(w, "", "", fmt.Sprintf("User %q not found.", userName), "")
				return
			}
			h.renderUsersPage(w, "", "", fmt.Sprintf("Failed to rotate key for user %q: %v", userName, err), "")
			return
		}
		h.renderUsersPage(w, rotatedRaw, rotatedHash, "", fmt.Sprintf("API key rotated for user %q.", userName))
		return
	}

	if action == "deactivate" {
		userName := strings.TrimSpace(r.FormValue("user_name"))
		if userName == "" {
			h.renderUsersPage(w, "", "", "User name is required for deactivation.", "")
			return
		}

		if err := h.authStore.DeactivateUser(userName); err != nil {
			if errors.Is(err, auth.ErrUserNotFound) {
				h.renderUsersPage(w, "", "", fmt.Sprintf("User %q not found.", userName), "")
				return
			}
			if errors.Is(err, auth.ErrUserDeactivated) {
				h.renderUsersPage(w, "", "", fmt.Sprintf("User %q is already deactivated.", userName), "")
				return
			}
			h.renderUsersPage(w, "", "", fmt.Sprintf("Failed to deactivate user %q: %v", userName, err), "")
			return
		}

		h.renderUsersPage(w, "", "", "", fmt.Sprintf("User %q deactivated.", userName))
		return
	}

	if action == "update" {
		userName := strings.TrimSpace(r.FormValue("user_name"))
		if userName == "" {
			h.renderUsersPage(w, "", "", "User name is required for updates.", "")
			return
		}

		uc, err := h.buildUserConfigFromForm(r)
		if err != nil {
			h.renderUsersPage(w, "", "", fmt.Sprintf("Invalid user settings for %q: %v", userName, err), "")
			return
		}
		if err := h.authStore.UpdateUser(userName, uc); err != nil {
			if errors.Is(err, auth.ErrUserNotFound) {
				h.renderUsersPage(w, "", "", fmt.Sprintf("User %q not found.", userName), "")
				return
			}
			h.renderUsersPage(w, "", "", fmt.Sprintf("Failed to update user %q: %v", userName, err), "")
			return
		}

		h.renderUsersPage(w, "", "", "", fmt.Sprintf("User %q updated.", userName))
		return
	}

	if action != "create" {
		h.renderUsers(w, r)
		return
	}

	userName := strings.TrimSpace(r.FormValue("user_name"))
	if userName == "" {
		h.renderUsersPage(w, "", "", "User name is required.", "")
		return
	}

	rawKey := generateAPIKey()
	hash := auth.HashAPIKey(rawKey)
	uc, err := h.buildUserConfigFromForm(r)
	if err != nil {
		h.renderUsersPage(w, "", "", fmt.Sprintf("Invalid user settings for %q: %v", userName, err), "")
		return
	}
	uc.APIKeyHash = hash
	if err := h.authStore.CreateUser(userName, uc); err != nil {
		if errors.Is(err, auth.ErrUserExists) {
			h.renderUsersPage(w, "", "", fmt.Sprintf("User %q already exists.", userName), "")
			return
		}
		h.renderUsersPage(w, "", "", fmt.Sprintf("Failed to create user %q: %v", userName, err), "")
		return
	}

	h.renderUsersPage(w, rawKey, hash, "", fmt.Sprintf("User %q created.", userName))
}

func (h *Handler) handleModelAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.httpError(w, http.StatusBadRequest, "invalid form")
		return
	}

	action := strings.TrimSpace(r.FormValue("action"))
	modelName := strings.TrimSpace(r.FormValue("model_name"))
	if modelName == "" {
		h.renderModelsPage(w, "Model name is required.", "")
		return
	}

	if action == "delete" {
		if err := h.deleteModel(modelName); err != nil {
			h.renderModelsPage(w, fmt.Sprintf("Failed to delete model %q: %v", modelName, err), "")
			return
		}
		if err := h.persistModelMutation("delete", modelName, nil); err != nil {
			h.renderModelsPage(w, fmt.Sprintf("Model %q removed in-memory but failed to persist mutation: %v", modelName, err), "")
			return
		}
		if err := h.persistModelPricing(); err != nil {
			h.renderModelsPage(w, fmt.Sprintf("Model %q deleted but failed to persist pricing changes: %v", modelName, err), "")
			return
		}
		if err := h.reloadModelRuntimeFromStore(); err != nil {
			h.renderModelsPage(w, fmt.Sprintf("Model %q deleted but failed to refresh runtime catalog: %v", modelName, err), "")
			return
		}
		h.renderModelsPage(w, "", fmt.Sprintf("Model %q deleted.", modelName))
		return
	}

	refs, err := parseModelBackendRefs(r.FormValue("backend_weights"), h.cfg.Backends)
	if err != nil {
		h.renderModelsPage(w, fmt.Sprintf("Invalid backend refs for model %q: %v", modelName, err), "")
		return
	}

	entry := config.ModelEntry{
		Name:     strings.TrimSpace(r.FormValue("display_name")),
		Backends: refs,
	}
	if entry.Name == "" {
		entry.Name = modelName
	}

	inputCost, outputCost, err := parseModelPricingForm(r)
	if err != nil {
		h.renderModelsPage(w, fmt.Sprintf("Invalid pricing for model %q: %v", modelName, err), "")
		return
	}

	limitAccess := strings.EqualFold(strings.TrimSpace(r.FormValue("limit_access")), "on")
	selectedUsers := make(map[string]bool)
	for _, userID := range r.Form["user_access"] {
		trimmed := strings.TrimSpace(userID)
		if trimmed != "" {
			selectedUsers[trimmed] = true
		}
	}

	switch action {
	case "create":
		if err := h.createModel(modelName, entry); err != nil {
			h.renderModelsPage(w, fmt.Sprintf("Failed to create model %q: %v", modelName, err), "")
			return
		}
	case "update":
		if err := h.updateModel(modelName, entry); err != nil {
			h.renderModelsPage(w, fmt.Sprintf("Failed to update model %q: %v", modelName, err), "")
			return
		}
	default:
		h.renderModelsPage(w, "Unknown model action.", "")
		return
	}

	h.setModelPricing(modelName, inputCost, outputCost)
	if err := h.applyModelAccess(modelName, limitAccess, selectedUsers); err != nil {
		h.renderModelsPage(w, fmt.Sprintf("Saved model %q but failed to apply user access: %v", modelName, err), "")
		return
	}
	persistedEntry := h.currentModelCatalog()[modelName]
	if err := h.persistModelMutation(action, modelName, &persistedEntry); err != nil {
		h.renderModelsPage(w, fmt.Sprintf("Saved model %q in-memory but failed to persist mutation: %v", modelName, err), "")
		return
	}
	if err := h.persistModelPricing(); err != nil {
		h.renderModelsPage(w, fmt.Sprintf("Saved model %q but failed to persist pricing changes: %v", modelName, err), "")
		return
	}
	if err := h.reloadModelRuntimeFromStore(); err != nil {
		h.renderModelsPage(w, fmt.Sprintf("Saved model %q but failed to refresh runtime catalog: %v", modelName, err), "")
		return
	}

	if action == "create" {
		h.renderModelsPage(w, "", fmt.Sprintf("Model %q created.", modelName))
		return
	}
	h.renderModelsPage(w, "", fmt.Sprintf("Model %q updated.", modelName))
}

func (h *Handler) createModel(modelName string, entry config.ModelEntry) error {
	catalog := h.currentModelCatalog()
	if _, exists := catalog[modelName]; exists {
		return fmt.Errorf("model already exists")
	}
	catalog[modelName] = entry
	h.syncModelToConfig(modelName, entry)
	return nil
}

func (h *Handler) updateModel(modelName string, entry config.ModelEntry) error {
	catalog := h.currentModelCatalog()
	if _, exists := catalog[modelName]; !exists {
		return fmt.Errorf("model not found")
	}
	catalog[modelName] = entry
	h.syncModelToConfig(modelName, entry)
	return nil
}

func (h *Handler) deleteModel(modelName string) error {
	catalog := h.currentModelCatalog()
	if _, exists := catalog[modelName]; !exists {
		return fmt.Errorf("model not found")
	}
	delete(catalog, modelName)
	if h.models != nil {
		delete(h.models, modelName)
	}
	if h.cfg.Models.Models != nil {
		delete(h.cfg.Models.Models, modelName)
	}

	if h.cfg.Pricing.Models != nil {
		delete(h.cfg.Pricing.Models, modelName)
	}

	// Keep user policy clean by removing references to deleted models.
	users := h.usersSnapshot()
	for userID, uc := range users {
		updated := false
		allow := removeCSVValue(uc.ModelAllow, modelName)
		if len(allow) != len(uc.ModelAllow) {
			uc.ModelAllow = allow
			updated = true
		}
		deny := removeCSVValue(uc.ModelDeny, modelName)
		if len(deny) != len(uc.ModelDeny) {
			uc.ModelDeny = deny
			updated = true
		}
		if updated {
			if err := h.authStore.UpdateUser(userID, uc); err != nil {
				return err
			}
		}
	}

	return nil
}

func (h *Handler) syncModelToConfig(modelName string, entry config.ModelEntry) {
	if h.cfg.Models.Models == nil {
		h.cfg.Models.Models = map[string]config.ModelEntry{}
	}
	h.cfg.Models.Models[modelName] = entry
}

func (h *Handler) persistModelMutation(action, modelName string, entry *config.ModelEntry) error {
	if h.modelStore == nil {
		return nil
	}

	switch action {
	case "create", "update":
		if entry == nil {
			return fmt.Errorf("missing model entry for %s", action)
		}
		if err := h.modelStore.UpsertModel(modelName, *entry); err != nil {
			return err
		}
	case "delete":
		if err := h.modelStore.DeactivateModel(modelName); err != nil {
			if errors.Is(err, models.ErrModelNotFound) {
				return fmt.Errorf("model not found")
			}
			return err
		}
	default:
		return fmt.Errorf("unknown model action %q", action)
	}

	return nil
}

func (h *Handler) persistModelPricing() error {
	if h.modelStore == nil {
		return nil
	}
	return h.modelStore.ReplaceModelPricing(h.cfg.Pricing.Models)
}

func (h *Handler) reloadModelRuntimeFromStore() error {
	if h.modelStore == nil {
		if h.refreshResolverCatalog != nil {
			h.refreshResolverCatalog(h.currentModelCatalog())
		}
		if h.refreshProxyPricing != nil {
			h.refreshProxyPricing(buildUsagePricingConfig(h.cfg.Pricing))
		}
		return nil
	}

	catalog, err := h.modelStore.LoadActiveCatalog()
	if err != nil {
		return err
	}
	h.SetModelCatalog(catalog)
	h.cfg.Models.Models = cloneConfigCatalog(catalog)

	pricing, err := h.modelStore.LoadModelPricing()
	if err != nil {
		return err
	}
	h.cfg.Pricing.Models = pricing

	if h.refreshResolverCatalog != nil {
		h.refreshResolverCatalog(catalog)
	}
	if h.refreshProxyPricing != nil {
		h.refreshProxyPricing(buildUsagePricingConfig(h.cfg.Pricing))
	}

	return nil
}

func (h *Handler) setModelPricing(modelName string, inputCost, outputCost float64) {
	if h.cfg.Pricing.Models == nil {
		h.cfg.Pricing.Models = map[string]config.ModelPricing{}
	}
	if inputCost == 0 && outputCost == 0 {
		delete(h.cfg.Pricing.Models, modelName)
		return
	}
	h.cfg.Pricing.Models[modelName] = config.ModelPricing{
		InputCostPer1M:  inputCost,
		OutputCostPer1M: outputCost,
	}
}

func (h *Handler) applyModelAccess(modelName string, limitAccess bool, selectedUsers map[string]bool) error {
	users := h.usersSnapshot()
	for userID, uc := range users {
		allowModel := true
		if limitAccess {
			allowModel = selectedUsers[userID]
		}

		if allowModel {
			uc.ModelDeny = removeCSVValue(uc.ModelDeny, modelName)
			if len(uc.ModelAllow) > 0 && !containsCSVValue(uc.ModelAllow, modelName) {
				uc.ModelAllow = append(uc.ModelAllow, modelName)
			}
		} else {
			uc.ModelAllow = removeCSVValue(uc.ModelAllow, modelName)
			if !containsCSVValue(uc.ModelDeny, modelName) {
				uc.ModelDeny = append(uc.ModelDeny, modelName)
			}
		}

		sort.Strings(uc.ModelAllow)
		sort.Strings(uc.ModelDeny)
		if err := h.authStore.UpdateUser(userID, uc); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) buildUserConfigFromForm(r *http.Request) (config.UserConfig, error) {
	allow, err := splitCSV(r.FormValue("model_allow"))
	if err != nil {
		return config.UserConfig{}, err
	}
	deny, err := splitCSV(r.FormValue("model_deny"))
	if err != nil {
		return config.UserConfig{}, err
	}
	aliases, err := parseAliases(r.FormValue("aliases"))
	if err != nil {
		return config.UserConfig{}, err
	}

	rateLimitEnabled := strings.EqualFold(strings.TrimSpace(r.FormValue("rate_limit_enabled")), "on")
	var rl *config.RateLimitCfg
	if rateLimitEnabled {
		rate, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("rate_limit_rate")), 64)
		if err != nil || rate <= 0 {
			return config.UserConfig{}, fmt.Errorf("rate limit rate must be a positive number")
		}
		burst, err := strconv.Atoi(strings.TrimSpace(r.FormValue("rate_limit_burst")))
		if err != nil || burst <= 0 {
			return config.UserConfig{}, fmt.Errorf("rate limit burst must be a positive integer")
		}
		ttlSeconds, err := strconv.Atoi(strings.TrimSpace(r.FormValue("rate_limit_ttl_seconds")))
		if err != nil || ttlSeconds <= 0 {
			return config.UserConfig{}, fmt.Errorf("rate limit TTL seconds must be a positive integer")
		}
		rl = &config.RateLimitCfg{
			Rate:  rate,
			Burst: burst,
			TTL:   time.Duration(ttlSeconds) * time.Second,
		}
	}

	return config.UserConfig{
		RateLimit:  rl,
		ModelAllow: allow,
		ModelDeny:  deny,
		Aliases:    aliases,
	}, nil
}

func splitCSV(input string) ([]string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return []string{}, nil
	}
	parts := strings.Split(trimmed, ",")
	result := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result, nil
}

func parseAliases(input string) (map[string]string, error) {
	aliases := map[string]string{}
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return aliases, nil
	}
	entries := strings.Split(trimmed, ",")
	for _, entry := range entries {
		item := strings.TrimSpace(entry)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("alias entry %q must use alias:model format", item)
		}
		alias := strings.TrimSpace(parts[0])
		model := strings.TrimSpace(parts[1])
		if alias == "" || model == "" {
			return nil, fmt.Errorf("alias entry %q must include both alias and model", item)
		}
		aliases[alias] = model
	}
	return aliases, nil
}

func formatAliases(aliases map[string]string) string {
	if len(aliases) == 0 {
		return ""
	}
	keys := make([]string, 0, len(aliases))
	for k := range aliases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	items := make([]string, 0, len(keys))
	for _, k := range keys {
		items = append(items, k+":"+aliases[k])
	}
	return strings.Join(items, ", ")
}

func userRateOrDefault(rl *config.RateLimitCfg, defaultRate float64) float64 {
	if rl != nil && rl.Rate > 0 {
		return rl.Rate
	}
	return defaultRate
}

func userBurstOrDefault(rl *config.RateLimitCfg, defaultBurst int) int {
	if rl != nil && rl.Burst > 0 {
		return rl.Burst
	}
	return defaultBurst
}

func userTTLOrDefaultSeconds(rl *config.RateLimitCfg, defaultTTL time.Duration) int {
	if rl != nil && rl.TTL > 0 {
		return int(rl.TTL / time.Second)
	}
	if defaultTTL <= 0 {
		return 0
	}
	return int(defaultTTL / time.Second)
}

func (h *Handler) usersSnapshot() map[string]config.UserConfig {
	users, err := h.authStore.ListUsers()
	if err != nil {
		fallback := make(map[string]config.UserConfig, len(h.cfg.Users))
		for userID, uc := range h.cfg.Users {
			fallback[userID] = uc
		}
		return fallback
	}
	return users
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

func (h *Handler) loadUserStats(userID string) (usage.UserStats, error) {
	if h.usageStore == nil {
		return usage.UserStats{}, fmt.Errorf("usage store not configured")
	}
	return h.usageStore.UserStats(userID, 5)
}

func generateAPIKey() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
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

func formatModelBackendRefs(refs []config.ModelBackendRef) string {
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Backend == "" {
			continue
		}
		weight := ref.Weight
		if weight <= 0 {
			weight = 1
		}
		parts = append(parts, fmt.Sprintf("%s:%d", ref.Backend, weight))
	}
	return strings.Join(parts, ", ")
}

func parseModelBackendRefs(input string, backends []config.Backend) ([]config.ModelBackendRef, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, fmt.Errorf("at least one backend:weight pair is required")
	}

	validBackends := make(map[string]bool, len(backends))
	for _, b := range backends {
		validBackends[b.Name] = true
	}

	parts := strings.Split(trimmed, ",")
	refs := make([]config.ModelBackendRef, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		pair := strings.SplitN(item, ":", 2)
		if len(pair) != 2 {
			return nil, fmt.Errorf("%q must use backend:weight format", item)
		}
		backendName := strings.TrimSpace(pair[0])
		if backendName == "" {
			return nil, fmt.Errorf("backend name cannot be empty")
		}
		if !validBackends[backendName] {
			return nil, fmt.Errorf("backend %q does not exist", backendName)
		}
		weight, err := strconv.Atoi(strings.TrimSpace(pair[1]))
		if err != nil || weight <= 0 {
			return nil, fmt.Errorf("weight for backend %q must be a positive integer", backendName)
		}
		if seen[backendName] {
			return nil, fmt.Errorf("backend %q is duplicated", backendName)
		}
		seen[backendName] = true
		refs = append(refs, config.ModelBackendRef{Backend: backendName, Weight: weight})
	}

	if len(refs) == 0 {
		return nil, fmt.Errorf("at least one backend:weight pair is required")
	}
	return refs, nil
}

func parseModelPricingForm(r *http.Request) (float64, float64, error) {
	inputCost := 0.0
	outputCost := 0.0

	inputRaw := strings.TrimSpace(r.FormValue("input_cost_per_1m_tokens"))
	if inputRaw != "" {
		parsed, err := strconv.ParseFloat(inputRaw, 64)
		if err != nil || parsed < 0 {
			return 0, 0, fmt.Errorf("input cost must be a non-negative number")
		}
		inputCost = parsed
	}

	outputRaw := strings.TrimSpace(r.FormValue("output_cost_per_1m_tokens"))
	if outputRaw != "" {
		parsed, err := strconv.ParseFloat(outputRaw, 64)
		if err != nil || parsed < 0 {
			return 0, 0, fmt.Errorf("output cost must be a non-negative number")
		}
		outputCost = parsed
	}

	return inputCost, outputCost, nil
}

func modelAccessibleForUser(uc config.UserConfig, modelName string) bool {
	if containsCSVValue(uc.ModelDeny, modelName) {
		return false
	}
	if len(uc.ModelAllow) == 0 {
		return true
	}
	return containsCSVValue(uc.ModelAllow, modelName)
}

func containsCSVValue(items []string, target string) bool {
	for _, item := range items {
		if strings.TrimSpace(item) == target {
			return true
		}
	}
	return false
}

func removeCSVValue(items []string, target string) []string {
	filtered := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item) == target {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func buildUsagePricingConfig(pricing config.PricingConfig) *usage.PricingConfig {
	out := &usage.PricingConfig{
		DefaultInputPer1M:  pricing.DefaultInputPer1M,
		DefaultOutputPer1M: pricing.DefaultOutputPer1M,
		ModelPricing:       make(map[string]usage.ModelPricing, len(pricing.Models)),
	}
	for modelName, mp := range pricing.Models {
		out.ModelPricing[modelName] = usage.ModelPricing{
			InputCostPer1M:  mp.InputCostPer1M,
			OutputCostPer1M: mp.OutputCostPer1M,
		}
	}
	return out
}

func cloneConfigCatalog(catalog map[string]config.ModelEntry) map[string]config.ModelEntry {
	cloned := make(map[string]config.ModelEntry, len(catalog))
	for modelName, entry := range catalog {
		refs := make([]config.ModelBackendRef, 0, len(entry.Backends))
		for _, ref := range entry.Backends {
			refs = append(refs, config.ModelBackendRef{Backend: ref.Backend, Weight: ref.Weight})
		}
		cloned[modelName] = config.ModelEntry{Name: entry.Name, Backends: refs}
	}
	return cloned
}
