package worker

import (
	_ "embed"
	"fmt"
	"os"
)

//go:embed agent.py
var AgentPySource string

type BootOptions struct {
	ModelID     string
	HFRepo      string
	HFFile      string
	ContextSize int
}

func currentAgentPy() string {
	if data, err := os.ReadFile("internal/worker/agent.py"); err == nil && len(data) > 100 {
		return string(data)
	}
	return AgentPySource
}

func GenerateBootScript(opts BootOptions) string {
	header := fmt.Sprintf(`#!/usr/bin/env python3
import os
os.environ.setdefault("FREEGPU_MODEL_ID", %q)
os.environ.setdefault("FREEGPU_HF_REPO", %q)
os.environ.setdefault("FREEGPU_HF_FILE", %q)
os.environ.setdefault("FREEGPU_CTX_SIZE", %q)
`,
		opts.ModelID,
		opts.HFRepo,
		opts.HFFile,
		fmt.Sprintf("%d", opts.ContextSize),
	)
	return header + "\n" + currentAgentPy()
}
