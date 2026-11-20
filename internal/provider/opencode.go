package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anilgoudgunda/freegpu/internal/config"
)

// BuildOpenCodeProviderBlock builds the standard `provider` definition for ~/.config/opencode/config.json.
func BuildOpenCodeProviderBlock(port int, activeModelID string) map[string]any {
	modelsMap := make(map[string]any)
	for _, p := range config.DefaultPresets() {
		modelsMap[p.ID] = map[string]any{
			"name":        p.Name,
			"attachment":  false,
			"reasoning":   false,
			"temperature": true,
			"tool_call":   true,
			"modalities": map[string]any{
				"input":  []string{"text"},
				"output": []string{"text"},
			},
		}
	}

	if _, ok := modelsMap[activeModelID]; !ok && activeModelID != "" {
		modelsMap[activeModelID] = map[string]any{
			"name":        activeModelID,
			"attachment":  false,
			"reasoning":   false,
			"temperature": true,
			"tool_call":   true,
			"modalities": map[string]any{
				"input":  []string{"text"},
				"output": []string{"text"},
			},
		}
	}

	return map[string]any{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "FreeGPU",
		"options": map[string]any{
			"baseURL": fmt.Sprintf("http://127.0.0.1:%d/v1", port),
			"apiKey":  "freegpu-local",
		},
		"models": modelsMap,
	}
}

// BuildOpenCodeJSONCProvidersBlock builds the `providers` definition used by opencode.jsonc (`@opencode/ai/providers/openai-compatible`).
func BuildOpenCodeJSONCProvidersBlock(port int, activeModelID string) map[string]any {
	modelsMap := make(map[string]any)
	for _, p := range config.DefaultPresets() {
		modelsMap[p.ID] = map[string]any{
			"modelID": p.ID,
			"name":    p.Name,
			"limit": map[string]any{
				"context": p.DefaultCtx,
				"output":  16384,
			},
			"capabilities": map[string]any{
				"tools": true,
			},
		}
	}

	if _, ok := modelsMap[activeModelID]; !ok && activeModelID != "" {
		modelsMap[activeModelID] = map[string]any{
			"modelID": activeModelID,
			"name":    activeModelID,
			"limit": map[string]any{
				"context": 131072,
				"output":  16384,
			},
			"capabilities": map[string]any{
				"tools": true,
			},
		}
	}

	return map[string]any{
		"name":    "FreeGPU",
		"env":     []string{"FREEGPU_API_KEY", "HOME"},
		"package": "@opencode/ai/providers/openai-compatible",
		"settings": map[string]any{
			"baseURL": fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		},
		"models": modelsMap,
	}
}

// GenerateOpenCodeSnippet returns a formatted JSON snippet for OpenCode configuration.
func GenerateOpenCodeSnippet(port int, activeModelID string) string {
	doc := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"model":   fmt.Sprintf("freegpu/%s", activeModelID),
		"providers": map[string]any{
			"freegpu": BuildOpenCodeJSONCProvidersBlock(port, activeModelID),
		},
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return string(b)
}

// InjectOpenCodeConfig safely merges the `freegpu` provider into ~/.config/opencode/opencode.jsonc, config.json, and ~/.local/share/opencode/auth.json.
func InjectOpenCodeConfig(port int, activeModelID string, setDefaultModel bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	ocDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(ocDir, 0755); err != nil {
		return "", err
	}

	// 1. Update opencode.jsonc if it exists (takes precedence in OpenCode)
	jsoncPath := filepath.Join(ocDir, "opencode.jsonc")
	cfgPath := filepath.Join(ocDir, "config.json")
	updatedPath := cfgPath

	if existingJSONC, err := os.ReadFile(jsoncPath); err == nil {
		_ = os.WriteFile(jsoncPath+".bak", existingJSONC, 0644)
		root := make(map[string]any)
		if json.Unmarshal(existingJSONC, &root) == nil {
			providers, ok := root["providers"].(map[string]any)
			if !ok || providers == nil {
				providers = make(map[string]any)
			}
			providers["freegpu"] = BuildOpenCodeJSONCProvidersBlock(port, activeModelID)
			root["providers"] = providers
			if setDefaultModel {
				root["model"] = fmt.Sprintf("freegpu/%s", activeModelID)
			}
			if out, err := json.MarshalIndent(root, "", "  "); err == nil {
				_ = os.WriteFile(jsoncPath, out, 0644)
				updatedPath = jsoncPath
			}
		}
	}

	// 2. Also update ~/.config/opencode/config.json
	root := make(map[string]any)
	if existing, err := os.ReadFile(cfgPath); err == nil {
		_ = os.WriteFile(cfgPath+".bak", existing, 0644)
		_ = json.Unmarshal(existing, &root)
	}
	if _, ok := root["$schema"]; !ok {
		root["$schema"] = "https://opencode.ai/config.json"
	}
	providerMap, ok := root["provider"].(map[string]any)
	if !ok || providerMap == nil {
		providerMap = make(map[string]any)
	}
	providerMap["freegpu"] = BuildOpenCodeProviderBlock(port, activeModelID)
	root["provider"] = providerMap
	if setDefaultModel {
		root["model"] = fmt.Sprintf("freegpu/%s", activeModelID)
	}
	if out, err := json.MarshalIndent(root, "", "  "); err == nil {
		_ = os.WriteFile(cfgPath, out, 0644)
	}

	// 3. Register `freegpu` in ~/.local/share/opencode/auth.json so OpenCode TUI marks it as authenticated
	authDir := filepath.Join(home, ".local", "share", "opencode")
	authPath := filepath.Join(authDir, "auth.json")
	if err := os.MkdirAll(authDir, 0755); err == nil {
		authMap := make(map[string]any)
		if existingAuth, err := os.ReadFile(authPath); err == nil {
			_ = json.Unmarshal(existingAuth, &authMap)
		}
		authMap["freegpu"] = map[string]string{
			"type": "api",
			"key":  "freegpu-local",
		}
		if authBytes, err := json.MarshalIndent(authMap, "", "  "); err == nil {
			_ = os.WriteFile(authPath, authBytes, 0600)
		}
	}

	return updatedPath, nil
}
