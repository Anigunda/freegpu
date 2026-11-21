package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anilgoudgunda/freegpu/internal/config"
	"github.com/anilgoudgunda/freegpu/internal/provider"
)

func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	activeID, _, _, _ := s.cfg.GetActiveModelSpec()
	now := time.Now().Unix()

	type modelObj struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	var list []modelObj
	seen := make(map[string]bool)

	if activeID != "" {
		list = append(list, modelObj{
			ID:      activeID,
			Object:  "model",
			Created: now,
			OwnedBy: "freegpu-colab",
		})
		seen[activeID] = true
	}

	for _, p := range config.DefaultPresets() {
		if !seen[p.ID] {
			list = append(list, modelObj{
				ID:      p.ID,
				Object:  "model",
				Created: now,
				OwnedBy: "freegpu-colab",
			})
			seen[p.ID] = true
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   list,
	})
}

func (s *Server) handleOpenAIProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Path == "/v1/models" && r.Method == http.MethodGet {
		s.handleOpenAIModels(w, r)
		return
	}

	// Strictly allowlist OpenAI inference paths so llama-server admin/metrics endpoints cannot be reached
	switch r.URL.Path {
	case "/v1/chat/completions", "/v1/completions", "/v1/embeddings":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
	default:
		http.Error(w, "unsupported OpenAI endpoint", http.StatusNotFound)
		return
	}

	s.cfg.RLock()
	var jupyterURL, proxyToken, status, detail string
	var workerFresh bool
	if s.cfg.ActiveWorker != nil {
		jupyterURL = s.cfg.ActiveWorker.JupyterURL
		proxyToken = s.cfg.ActiveWorker.ProxyToken
		status = s.cfg.ActiveWorker.Status
		detail = s.cfg.ActiveWorker.StatusDetail
		workerFresh = time.Since(s.cfg.ActiveWorker.LastSeen) < 60*time.Second
	}
	s.cfg.RUnlock()

	if jupyterURL == "" || !workerFresh {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "Colab GPU is offline. Click [START GPU] in the sidebar first.",
				"type":    "gpu_offline",
			},
		})
		return
	}

	if status != "ready" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("Colab GPU is warming up (%s: %s). Retry in a few seconds.", strings.ToUpper(status), detail),
				"type":    "gpu_warming_up",
			},
		})
		return
	}

	startTime := time.Now()
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20) // 64 MiB cap (plenty for 128k context prompts)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = r.Body.Close()

	isStream := bytes.Contains(bodyBytes, []byte(`"stream":true`)) || bytes.Contains(bodyBytes, []byte(`"stream": true`))
	if isStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}

	targetPath := r.URL.Path
	if r.URL.RawQuery != "" {
		targetPath += "?" + r.URL.RawQuery
	}

	flusher, canFlush := w.(http.Flusher)
	var firstByteTime time.Time
	var chunkEvents int64
	var tailBuf bytes.Buffer

	_, streamErr := provider.StreamOpenAIOverColabKernel(r.Context(), jupyterURL, proxyToken, r.Method, targetPath, bodyBytes, func(chunk []byte) {
		if firstByteTime.IsZero() {
			firstByteTime = time.Now()
		}
		chunkEvents += int64(bytes.Count(chunk, []byte("data: {")))
		// Keep last 2KB to parse llama-server's exact `predicted_per_second` timing field
		if tailBuf.Len() > 4096 {
			b := tailBuf.Bytes()
			tailBuf.Reset()
			tailBuf.Write(b[len(b)-2048:])
		}
		tailBuf.Write(chunk)
		_, _ = w.Write(chunk)
		if canFlush {
			flusher.Flush()
		}
	})

	if streamErr != nil && firstByteTime.IsZero() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "Colab kernel stream error: " + streamErr.Error(),
			},
		})
		return
	}

	// Try extracting exact `predicted_per_second` from llama-server's timings payload
	exactTPS := extractPredictedPerSecond(tailBuf.String())

	elapsed := time.Since(startTime)
	genWindow := elapsed
	if !firstByteTime.IsZero() {
		genWindow = time.Since(firstByteTime)
		if genWindow < 50*time.Millisecond {
			genWindow = elapsed
		}
	}
	if chunkEvents == 0 {
		chunkEvents = 28
	}

	s.cfg.Lock()
	s.cfg.Metrics.TotalRequests++
	s.cfg.Metrics.TotalTokens += chunkEvents
	if !firstByteTime.IsZero() {
		s.cfg.Metrics.LastLatencyMs = firstByteTime.Sub(startTime).Milliseconds()
	}
	if exactTPS > 0 {
		s.cfg.Metrics.LastTokensPerSec = float64(int(exactTPS*10)) / 10.0
	} else if genWindow.Seconds() > 0.05 && chunkEvents > 1 {
		tps := float64(chunkEvents) / genWindow.Seconds()
		s.cfg.Metrics.LastTokensPerSec = float64(int(tps*10)) / 10.0
	}
	s.cfg.Unlock()
}

func extractPredictedPerSecond(s string) float64 {
	const key = `"predicted_per_second":`
	idx := strings.LastIndex(s, key)
	if idx == -1 {
		return 0
	}
	rest := strings.TrimSpace(s[idx+len(key):])
	var val float64
	if _, err := fmt.Sscanf(rest, "%f", &val); err == nil && val > 0 {
		return val
	}
	return 0
}
