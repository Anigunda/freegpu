package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ModelPreset defines a curated GGUF coding model for OpenCode + llama.cpp.
type ModelPreset struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	HFRepo        string  `json:"hf_repo"`
	HFFile        string  `json:"hf_file"`
	SizeGB        float64 `json:"size_gb"`
	MinVRAMGB     int     `json:"min_vram_gb"`
	DefaultCtx    int     `json:"default_ctx"`
	RecommendedOn string  `json:"recommended_on"`
}

func DefaultPresets() []ModelPreset {
	return []ModelPreset{
		{
			ID:            "qwen3.6-35b-a3b",
			Name:          "Qwen 3.6 35B",
			HFRepo:        "unsloth/Qwen3.6-35B-A3B-GGUF",
			HFFile:        "Qwen3.6-35B-A3B-UD-Q4_K_M.gguf",
			SizeGB:        20.6,
			MinVRAMGB:     22,
			DefaultCtx:    131072,
			RecommendedOn: "A100 (80GB) / L4 (24GB)",
		},
		{
			ID:            "inkling-2-mini-q8",
			Name:          "Inkling 2 Mini Q8",
			HFRepo:        "unsloth/Inkling-2-Mini-GGUF",
			HFFile:        "Inkling-2-Mini-UD-Q8_K_XL.gguf",
			SizeGB:        21.3,
			MinVRAMGB:     26,
			DefaultCtx:    131072,
			RecommendedOn: "A100 (80GB VRAM)",
		},
		{
			ID:            "inkling-2-mini",
			Name:          "Inkling 2 Mini Q4-XL",
			HFRepo:        "unsloth/Inkling-2-Mini-GGUF",
			HFFile:        "Inkling-2-Mini-UD-Q4_K_XL.gguf",
			SizeGB:        16.4,
			MinVRAMGB:     19,
			DefaultCtx:    131072,
			RecommendedOn: "L4 (24GB) / A100 (80GB)",
		},
		{
			ID:            "qwen3-coder-next",
			Name:          "Qwen 3 Coder Next 80B",
			HFRepo:        "unsloth/Qwen3-Coder-Next-GGUF",
			HFFile:        "Qwen3-Coder-Next-UD-Q4_K_S.gguf",
			SizeGB:        42.9,
			MinVRAMGB:     48,
			DefaultCtx:    131072,
			RecommendedOn: "A100 (80GB VRAM)",
		},
		{
			ID:            "qwen3-coder-30b-a3b",
			Name:          "Qwen 3 Coder 30B",
			HFRepo:        "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF",
			HFFile:        "Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf",
			SizeGB:        17.3,
			MinVRAMGB:     20,
			DefaultCtx:    131072,
			RecommendedOn: "A100 / L4 (24GB)",
		},
		{
			ID:            "nemotron-3.5-30b-a3b",
			Name:          "Nemotron 3.5 30B",
			HFRepo:        "unsloth/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-GGUF",
			HFFile:        "NVIDIA-Nemotron-3.5-Lightning-30B-A3B-MXFP4_MOE.gguf",
			SizeGB:        21.6,
			MinVRAMGB:     24,
			DefaultCtx:    131072,
			RecommendedOn: "A100 (80GB) / L4 (24GB)",
		},
		{
			ID:            "glm-4.7-flash",
			Name:          "GLM 4.7 Flash 30B",
			HFRepo:        "unsloth/GLM-4.7-Flash-GGUF",
			HFFile:        "GLM-4.7-Flash-Q4_K_M.gguf",
			SizeGB:        17.1,
			MinVRAMGB:     20,
			DefaultCtx:    131072,
			RecommendedOn: "A100 / L4 (24GB)",
		},
		{
			ID:            "gpt-oss-20b",
			Name:          "gpt-oss 20B",
			HFRepo:        "unsloth/gpt-oss-20b-GGUF",
			HFFile:        "gpt-oss-20b-Q4_K_M.gguf",
			SizeGB:        10.8,
			MinVRAMGB:     14,
			DefaultCtx:    131072,
			RecommendedOn: "T4 (16GB) / L4 / A100",
		},
		{
			ID:            "devstral-small-2-24b",
			Name:          "Devstral Small 2 24B",
			HFRepo:        "unsloth/Devstral-Small-2-24B-Instruct-2512-GGUF",
			HFFile:        "Devstral-Small-2-24B-Instruct-2512-Q4_K_M.gguf",
			SizeGB:        13.3,
			MinVRAMGB:     16,
			DefaultCtx:    131072,
			RecommendedOn: "L4 (24GB) / A100 (80GB)",
		},
		{
			ID:            "qwen2.5-coder-32b",
			Name:          "Qwen 2.5 Coder 32B",
			HFRepo:        "bartowski/Qwen2.5-Coder-32B-Instruct-GGUF",
			HFFile:        "Qwen2.5-Coder-32B-Instruct-Q4_K_M.gguf",
			SizeGB:        19.8,
			MinVRAMGB:     22,
			DefaultCtx:    32768,
			RecommendedOn: "A100 (80GB) / L4 (24GB)",
		},
	}
}

type GPUDevice struct {
	Index       int     `json:"index"`
	Name        string  `json:"name"`
	VRAMUsedMB  float64 `json:"vram_used_mb"`
	VRAMTotalMB float64 `json:"vram_total_mb"`
	UtilPercent float64 `json:"util_percent"`
	TempC       float64 `json:"temp_c"`
	PowerWatts  float64 `json:"power_watts"`
}

type WorkerTelemetry struct {
	RuntimeName   string      `json:"runtime_name"`
	JupyterURL    string      `json:"jupyter_url"`
	ProxyToken    string      `json:"-"` // Never persist Colab JWT proxy token to disk or expose via /api/state
	Status        string      `json:"status"` // "booting", "downloading", "loading_model", "ready", "error"
	StatusDetail  string      `json:"status_detail"`
	ActiveModel   string      `json:"active_model"`
	HFRepo        string      `json:"hf_repo"`
	HFFile        string      `json:"hf_file"`
	ContextSize   int         `json:"context_size"`
	GPUs          []GPUDevice `json:"gpus"`
	UptimeSeconds float64     `json:"uptime_seconds"`
	LlamaHealthy  bool        `json:"llama_healthy"`
	LastSeen      time.Time   `json:"last_seen"`
	RecentLogs    []string    `json:"recent_logs,omitempty"`
}

type QuotaState struct {
	ColabTotalCU       float64 `json:"colab_total_cu"`
	ColabRemainingCU   float64 `json:"colab_remaining_cu"`
	LiveBurnRateHourly float64 `json:"live_burn_rate_hourly"`
	SyncedFromColab    bool    `json:"synced_from_colab"`
	ColabBurnRateT4    float64 `json:"colab_burn_rate_t4"`
	ColabBurnRateL4    float64 `json:"colab_burn_rate_l4"`
	ColabBurnRateA100  float64 `json:"colab_burn_rate_a100"`
}

type ProxyMetrics struct {
	TotalRequests    int64   `json:"total_requests"`
	TotalTokens      int64   `json:"total_tokens"`
	LastTokensPerSec float64 `json:"last_tokens_per_sec"`
	LastLatencyMs    int64   `json:"last_latency_ms"`
}

type AppConfig struct {
	mu sync.RWMutex

	Port              int              `json:"port"`
	PreferredGPU      string           `json:"preferred_gpu"` // "A100", "L4", "G4", "T4"
	ActiveModelPreset string           `json:"active_model_preset"`
	ContextSize       int              `json:"context_size"`
	Quota             QuotaState       `json:"quota"`
	Metrics           ProxyMetrics     `json:"metrics"`
	ActiveWorker      *WorkerTelemetry `json:"active_worker,omitempty"`
	LastAccountedTick time.Time        `json:"-"`
}

func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".freegpu"
	}
	return filepath.Join(home, ".freegpu")
}

func ConfigFilePath() string {
	return filepath.Join(ConfigDir(), "config.json")
}

func LoadOrCreate() (*AppConfig, error) {
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		return nil, err
	}
	_ = os.Chmod(ConfigDir(), 0700)

	cfg := &AppConfig{
		Port:              11435,
		PreferredGPU:      "A100",
		ActiveModelPreset: "qwen3.6-35b-a3b",
		ContextSize:       131072,
		Quota: QuotaState{
			ColabTotalCU:      100.0,
			ColabRemainingCU:  100.0,
			ColabBurnRateT4:   1.96,
			ColabBurnRateL4:   4.82,
			ColabBurnRateA100: 11.76,
		},
	}

	if data, err := os.ReadFile(ConfigFilePath()); err == nil {
		_ = json.Unmarshal(data, cfg)
	}

	if cfg.Port == 0 {
		cfg.Port = 11435
	}
	if cfg.PreferredGPU == "" {
		cfg.PreferredGPU = "A100"
	}
	if cfg.ActiveModelPreset == "" {
		cfg.ActiveModelPreset = "qwen3.6-35b-a3b"
	}
	if cfg.ContextSize <= 32768 {
		cfg.ContextSize = 131072
	}
	if cfg.Quota.ColabBurnRateT4 <= 0 {
		cfg.Quota.ColabBurnRateT4 = 1.96
		cfg.Quota.ColabBurnRateL4 = 4.82
		cfg.Quota.ColabBurnRateA100 = 11.76
	}
	if cfg.Quota.ColabTotalCU <= 0 {
		cfg.Quota.ColabTotalCU = 100.0
	}

	_ = cfg.Save()
	return cfg, nil
}

func (c *AppConfig) Save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.saveLocked()
}

func (c *AppConfig) saveLocked() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigFilePath(), data, 0600)
}

func (c *AppConfig) Lock()    { c.mu.Lock() }
func (c *AppConfig) Unlock()  { c.mu.Unlock() }
func (c *AppConfig) RLock()   { c.mu.RLock() }
func (c *AppConfig) RUnlock() { c.mu.RUnlock() }

func (c *AppConfig) GetActiveModelSpec() (id, hfRepo, hfFile string, ctxSize int) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ctxSize = c.ContextSize
	if ctxSize <= 0 {
		ctxSize = 32768
	}
	for _, p := range DefaultPresets() {
		if p.ID == c.ActiveModelPreset {
			return p.ID, p.HFRepo, p.HFFile, ctxSize
		}
	}
	p := DefaultPresets()[0]
	return p.ID, p.HFRepo, p.HFFile, ctxSize
}

func (c *AppConfig) UpdateTelemetry(t WorkerTelemetry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	t.LastSeen = now

	if c.ActiveWorker != nil && !c.LastAccountedTick.IsZero() {
		elapsedHours := now.Sub(c.LastAccountedTick).Hours()
		if elapsedHours > 0 && elapsedHours < (2.0/60.0) {
			burnRate := c.Quota.ColabBurnRateL4
			for _, g := range t.GPUs {
				if containsIgnoreCase(g.Name, "A100") {
					burnRate = c.Quota.ColabBurnRateA100
					break
				} else if containsIgnoreCase(g.Name, "T4") {
					burnRate = c.Quota.ColabBurnRateT4
					break
				}
			}
			c.Quota.ColabRemainingCU -= elapsedHours * burnRate
			if c.Quota.ColabRemainingCU < 0 {
				c.Quota.ColabRemainingCU = 0
			}
		}
	}

	c.LastAccountedTick = now
	c.ActiveWorker = &t
	_ = c.saveLocked()
}

func containsIgnoreCase(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			c1, c2 := s[i+j], sub[j]
			if c1 >= 'A' && c1 <= 'Z' {
				c1 += 32
			}
			if c2 >= 'A' && c2 <= 'Z' {
				c2 += 32
			}
			if c1 != c2 {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
