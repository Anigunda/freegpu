package server

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/anilgoudgunda/freegpu/internal/auth"
	"github.com/anilgoudgunda/freegpu/internal/config"
	"github.com/anilgoudgunda/freegpu/internal/provider"
	"github.com/anilgoudgunda/freegpu/internal/pty"
	"github.com/anilgoudgunda/freegpu/internal/worker"
)

//go:embed ui/index.html
var dashboardHTML []byte

type Server struct {
	cfg *config.AppConfig
}

func New(cfg *config.AppConfig) *Server {
	s := &Server{cfg: cfg}

	// Ensure OpenCode is always auto-configured on startup
	modelID, _, _, _ := cfg.GetActiveModelSpec()
	_, _ = provider.InjectOpenCodeConfig(cfg.Port, modelID, true)

	go s.backgroundSyncLoop()
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// OpenAI-compatible API for OpenCode
	mux.HandleFunc("/v1/", s.handleOpenAIProxy)
	mux.HandleFunc("/v1/models", s.handleOpenAIModels)

	// Embedded PTY WebSocket for interactive CLI / OpenCode terminal
	mux.HandleFunc("/api/pty", pty.HandleWebSocket)

	// Native Google OAuth2 PKCE endpoints (zero gcloud CLI required)
	mux.HandleFunc("/auth/login", s.handleAuthLogin)
	mux.HandleFunc("/auth/callback", auth.HandleCallback)
	mux.HandleFunc("/auth/status", s.handleAuthStatus)
	mux.HandleFunc("/auth/logout", s.handleAuthLogout)

	// Control Plane REST API
	mux.HandleFunc("/api/state", s.handleGetState)
	mux.HandleFunc("/api/model", s.handleUpdateModel)
	mux.HandleFunc("/api/quota", s.handleUpdateQuota)
	mux.HandleFunc("/api/colab/status", s.handleColabAPIStatus)
	mux.HandleFunc("/api/colab/login", s.handleColabAPILogin)
	mux.HandleFunc("/api/colab/up", s.handleColabAPIUp)
	mux.HandleFunc("/api/colab/down", s.handleColabAPIDown)

	// Embedded Desktop App UI
	mux.HandleFunc("/", s.handleDashboardUI)

	return mux
}

func (s *Server) currentBootOptions() worker.BootOptions {
	id, repo, file, ctx := s.cfg.GetActiveModelSpec()
	return worker.BootOptions{
		ModelID:     id,
		HFRepo:      repo,
		HFFile:      file,
		ContextSize: ctx,
	}
}

func (s *Server) handleDashboardUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(dashboardHTML)
}

func (s *Server) handleGetState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	modelID, hfRepo, hfFile, ctxSize := s.cfg.GetActiveModelSpec()

	s.cfg.RLock()
	var workerCopy *config.WorkerTelemetry
	var workerOnline bool
	if s.cfg.ActiveWorker != nil {
		cp := *s.cfg.ActiveWorker
		workerCopy = &cp
		workerOnline = time.Since(cp.LastSeen) < 45*time.Second
	}
	quota := s.cfg.Quota
	metrics := s.cfg.Metrics
	port := s.cfg.Port
	prefGPU := s.cfg.PreferredGPU
	activePreset := s.cfg.ActiveModelPreset
	s.cfg.RUnlock()

	colabHoursT4 := 0.0
	colabHoursL4 := 0.0
	colabHoursA100 := 0.0
	if quota.ColabBurnRateT4 > 0 {
		colabHoursT4 = quota.ColabRemainingCU / quota.ColabBurnRateT4
	}
	if quota.ColabBurnRateL4 > 0 {
		colabHoursL4 = quota.ColabRemainingCU / quota.ColabBurnRateL4
	}
	if quota.ColabBurnRateA100 > 0 {
		colabHoursA100 = quota.ColabRemainingCU / quota.ColabBurnRateA100
	}

	resp := map[string]any{
		"port":                port,
		"local_openai_url":    fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		"preferred_gpu":       prefGPU,
		"active_model_preset": activePreset,
		"active_model_id":     modelID,
		"active_hf_repo":      hfRepo,
		"active_hf_file":      hfFile,
		"context_size":        ctxSize,
		"presets":             config.DefaultPresets(),
		"worker_online":       workerOnline,
		"active_worker":       workerCopy,
		"quota":               quota,
		"computed_quota": map[string]any{
			"colab_hours_t4":   round1(colabHoursT4),
			"colab_hours_l4":   round1(colabHoursL4),
			"colab_hours_a100": round1(colabHoursA100),
		},
		"metrics": metrics,
	}

	_ = json.NewEncoder(w).Encode(resp)
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10.0
}

func (s *Server) handleUpdateModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Preset       string `json:"preset"`
		PreferredGPU string `json:"preferred_gpu"`
		ContextSize  int    `json:"context_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.cfg.Lock()
	if req.Preset != "" {
		s.cfg.ActiveModelPreset = req.Preset
	}
	if req.PreferredGPU != "" {
		s.cfg.PreferredGPU = strings.ToUpper(strings.TrimSpace(req.PreferredGPU))
	}
	if req.ContextSize > 0 {
		s.cfg.ContextSize = req.ContextSize
	}
	s.cfg.Unlock()
	_ = s.cfg.Save()

	modelID, repo, file, ctx := s.cfg.GetActiveModelSpec()
	_, _ = provider.InjectOpenCodeConfig(s.cfg.Port, modelID, true)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":           true,
		"model_id":     modelID,
		"hf_repo":      repo,
		"hf_file":      file,
		"context_size": ctx,
	})
}

func (s *Server) handleUpdateQuota(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ColabRemainingCU *float64 `json:"colab_remaining_cu"`
		ColabTotalCU     *float64 `json:"colab_total_cu"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.cfg.Lock()
	if req.ColabRemainingCU != nil {
		s.cfg.Quota.ColabRemainingCU = *req.ColabRemainingCU
	}
	if req.ColabTotalCU != nil && *req.ColabTotalCU > 0 {
		s.cfg.Quota.ColabTotalCU = *req.ColabTotalCU
	}
	s.cfg.Unlock()
	_ = s.cfg.Save()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleColabAPIStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(provider.InspectColabAPI())
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	authURL, err := auth.StartLogin(s.cfg.Port)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(auth.Status())
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	_ = auth.Logout()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleColabAPILogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := provider.TriggerColabOAuthLogin(s.cfg.Port); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"message": "Complete Google sign-in in browser",
	})
}

func (s *Server) handleColabAPIUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Accelerator string `json:"accelerator"`
		Preset      string `json:"preset"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	s.cfg.Lock()
	if req.Accelerator != "" {
		s.cfg.PreferredGPU = strings.ToUpper(strings.TrimSpace(req.Accelerator))
	}
	if req.Preset != "" {
		s.cfg.ActiveModelPreset = req.Preset
	}
	acc := s.cfg.PreferredGPU
	s.cfg.Unlock()
	_ = s.cfg.Save()

	modelID, _, _, _ := s.cfg.GetActiveModelSpec()
	_, _ = provider.InjectOpenCodeConfig(s.cfg.Port, modelID, true)

	// Immediately mark worker as booting so UI shows instant feedback
	s.cfg.UpdateTelemetry(config.WorkerTelemetry{
		Status:       "booting",
		StatusDetail: fmt.Sprintf("Allocating Colab %s GPU...", acc),
		ActiveModel:  modelID,
	})

	opts := s.currentBootOptions()
	bootScript := worker.GenerateBootScript(opts)
	rt, err := provider.SpinUpColabRuntimeHeadless(acc, bootScript)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		s.cfg.UpdateTelemetry(config.WorkerTelemetry{
			Status:       "error",
			StatusDetail: err.Error(),
			ActiveModel:  modelID,
		})
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}

	s.cfg.UpdateTelemetry(config.WorkerTelemetry{
		RuntimeName:  rt.Name,
		JupyterURL:   rt.ConnectionInfo.URL,
		ProxyToken:   rt.ConnectionInfo.Token,
		Status:       "booting",
		StatusDetail: fmt.Sprintf("%s allocated // starting llama-server...", acc),
		ActiveModel:  modelID,
	})

	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"runtime": rt,
		"message": fmt.Sprintf("Colab %s GPU allocated (%s)", acc, rt.Name),
	})
}

func (s *Server) handleColabAPIDown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	count, err := provider.DeleteColabRuntimes("")
	s.cfg.Lock()
	s.cfg.ActiveWorker = nil
	s.cfg.Unlock()
	_ = s.cfg.Save()

	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"deleted": count,
		"message": fmt.Sprintf("Stopped %d Colab GPU runtime(s)", count),
	})
}

func (s *Server) syncLiveColabCCU() {
	info, err := provider.FetchColabCCUInfo()
	if err != nil || info == nil {
		return
	}
	s.cfg.Lock()
	s.cfg.Quota.ColabRemainingCU = info.CurrentBalance
	s.cfg.Quota.LiveBurnRateHourly = info.ConsumptionRateHourly
	s.cfg.Quota.SyncedFromColab = true
	if info.CurrentBalance > s.cfg.Quota.ColabTotalCU {
		// Round up total bar capacity cleanly (e.g. 323 -> 400 or 500)
		if info.CurrentBalance <= 500 {
			s.cfg.Quota.ColabTotalCU = 500
		} else {
			s.cfg.Quota.ColabTotalCU = float64(int(info.CurrentBalance/100)+1) * 100
		}
	}
	s.cfg.Unlock()
	_ = s.cfg.Save()
}

func (s *Server) backgroundSyncLoop() {
	// Fetch real Google Colab Compute Units balance immediately on startup
	go s.syncLiveColabCCU()

	ticker := time.NewTicker(2500 * time.Millisecond)
	ccuTicker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	defer ccuTicker.Stop()

	for {
		select {
		case <-ccuTicker.C:
			go s.syncLiveColabCCU()
		case <-ticker.C:
			s.cfg.RLock()
			var rtName, jURL, pTok string
			if s.cfg.ActiveWorker != nil {
				rtName = s.cfg.ActiveWorker.RuntimeName
				jURL = s.cfg.ActiveWorker.JupyterURL
				pTok = s.cfg.ActiveWorker.ProxyToken
			}
			s.cfg.RUnlock()

			if t, err := provider.FetchDirectColabTelemetry(rtName, jURL, pTok); err == nil && t.Status != "" {
				s.cfg.UpdateTelemetry(t)
			}
		}
	}
}
