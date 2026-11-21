package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/anilgoudgunda/freegpu/internal/config"
	"github.com/anilgoudgunda/freegpu/internal/desktop"
	"github.com/anilgoudgunda/freegpu/internal/provider"
	"github.com/anilgoudgunda/freegpu/internal/server"
	"github.com/anilgoudgunda/freegpu/internal/worker"
)

const version = "1.0.0"

func init() {
	// Required by macOS AppKit / Cocoa NSApplication main loop
	runtime.LockOSThread()
}

func main() {
	if len(os.Args) < 2 {
		runDesktopApp()
		return
	}

	cmd := os.Args[1]
	switch cmd {
	case "app", "gui":
		runDesktopApp()
	case "serve":
		runServe(os.Args[2:])
	case "up", "colab-up":
		runColabUp(os.Args[2:])
	case "down", "colab-down":
		runColabDown()
	case "login", "colab-login":
		if err := provider.TriggerColabOAuthLogin(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("🔑 Opened Google OAuth consent screen in browser.")
	case "status":
		runStatus()
	case "models":
		runModels()
	case "use":
		runUseModel(os.Args[2:])
	case "opencode":
		cfg, err := config.LoadOrCreate()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		modelID, _, _, _ := cfg.GetActiveModelSpec()
		path, err := provider.InjectOpenCodeConfig(cfg.Port, modelID, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Synced FreeGPU provider (%s) into %s\n", modelID, path)
	case "version", "--version", "-v":
		fmt.Printf("freegpu v%s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	case "help", "--help", "-h":
		printHelp()
	default:
		// Ignore macOS Finder -psn_0_... launch flag when opened as a .app bundle
		if strings.HasPrefix(cmd, "-psn_") {
			runDesktopApp()
			return
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Printf(`⚡ FreeGPU v%s — Google Colab GPU Desktop App & OpenCode Gateway (llama.cpp)

USAGE:
  freegpu [app]                 Launch native macOS Desktop App (GPU Stats + Embedded Terminal)
  freegpu serve                 Run background daemon only (http://127.0.0.1:11435)
  freegpu up [--gpu A100|L4|T4] Allocate headless Colab GPU & start llama-server
  freegpu down                  Terminate active Colab GPU runtime to save Compute Units
  freegpu status                Show GPU status, VRAM & remaining Compute Units
  freegpu models                List curated coding models (128k MoE & Dense)
  freegpu use <model-id>        Switch default coding model
  freegpu opencode              Sync FreeGPU provider into OpenCode config
  freegpu login                 Sign in with Google for Colab GPU access
`, version)
}

func ensureServerRunning(cfg *config.AppConfig) string {
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	baseURL := "http://" + addr

	// Check if server is already listening on addr
	conn, err := net.DialTimeout("tcp", addr, 400*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		if tokBytes, err := os.ReadFile(server.SessionTokenFile()); err == nil {
			if tok := strings.TrimSpace(string(tokBytes)); tok != "" {
				return baseURL + "/?token=" + tok
			}
		}
		return baseURL
	}

	srv := server.New(cfg)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = httpSrv.ListenAndServe()
	}()

	// Wait briefly until listening
	for i := 0; i < 20; i++ {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return baseURL + "/?token=" + srv.SessionToken()
}

func runDesktopApp() {
	cfg, err := config.LoadOrCreate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		os.Exit(1)
	}
	url := ensureServerRunning(cfg)
	desktop.OpenWindow(url)
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	portFlag := fs.Int("port", 0, "Local port (default 11435)")
	_ = fs.Parse(args)

	cfg, err := config.LoadOrCreate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if *portFlag > 0 {
		cfg.Port = *portFlag
		_ = cfg.Save()
	}

	srv := server.New(cfg)
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	modelID, _, _, _ := cfg.GetActiveModelSpec()

	fmt.Printf("⚡ FreeGPU v%s listening on http://%s/?token=%s (OpenCode: http://%s/v1, Model: %s)\n", version, addr, srv.SessionToken(), addr, modelID)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func runStatus() {
	cfg, err := config.LoadOrCreate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	modelID, repo, file, ctx := cfg.GetActiveModelSpec()
	q := cfg.Quota

	fmt.Println("⚡ FreeGPU Status (Google Colab Direct)")
	fmt.Println("──────────────────────────────────────────────────────────────")
	fmt.Printf("  OpenCode Endpoint : http://127.0.0.1:%d/v1\n", cfg.Port)
	fmt.Printf("  Preferred GPU     : %s\n", cfg.PreferredGPU)
	fmt.Printf("  Selected Model    : %s (%s/%s, ctx=%d)\n", modelID, repo, file, ctx)

	if cfg.ActiveWorker != nil && time.Since(cfg.ActiveWorker.LastSeen) < 60*time.Second {
		w := cfg.ActiveWorker
		fmt.Printf("  Colab GPU Worker  : %s — %s\n", strings.ToUpper(w.Status), w.StatusDetail)
		for _, g := range w.GPUs {
			fmt.Printf("  GPU %d             : %s (%.1f / %.1f GB VRAM, %.0f%% util, %.0f°C)\n",
				g.Index, g.Name, g.VRAMUsedMB/1024.0, g.VRAMTotalMB/1024.0, g.UtilPercent, g.TempC)
		}
	} else {
		fmt.Println("  Colab GPU Worker  : STANDBY")
	}

	fmt.Printf("\n📊 Colab Compute Units: %.1f / %.0f CU remaining\n", q.ColabRemainingCU, q.ColabTotalCU)
	fmt.Printf("   ↳ Est. Hours: A100 (40GB): ~%.1fh | L4 (24GB): ~%.1fh | T4 (16GB): ~%.1fh\n",
		q.ColabRemainingCU/q.ColabBurnRateA100,
		q.ColabRemainingCU/q.ColabBurnRateL4,
		q.ColabRemainingCU/q.ColabBurnRateT4,
	)
}

func runModels() {
	cfg, _ := config.LoadOrCreate()
	activeID, _, _, _ := cfg.GetActiveModelSpec()
	fmt.Println("📦 Curated Coding Models (llama.cpp GGUF):")
	fmt.Println("────────────────────────────────────────────────────────────────────────")
	for _, p := range config.DefaultPresets() {
		marker := "  "
		if p.ID == activeID {
			marker = "* "
		}
		fmt.Printf("%s%-24s  %5.1f GB  Min VRAM: %2dGB  (%s)\n", marker, p.ID, p.SizeGB, p.MinVRAMGB, p.RecommendedOn)
	}
}

func runUseModel(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: freegpu use <model-id>")
		runModels()
		os.Exit(1)
	}
	presetID := strings.TrimSpace(args[0])
	cfg, err := config.LoadOrCreate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	cfg.ActiveModelPreset = presetID
	_ = cfg.Save()
	_, _ = provider.InjectOpenCodeConfig(cfg.Port, presetID, true)
	fmt.Printf("✅ Switched default model to %s\n", presetID)
}

func runColabUp(args []string) {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	gpu := fs.String("gpu", "", "Colab GPU accelerator: A100, L4, G4, or T4")
	_ = fs.Parse(args)

	cfg, err := config.LoadOrCreate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	acc := cfg.PreferredGPU
	if *gpu != "" {
		acc = strings.ToUpper(*gpu)
		cfg.PreferredGPU = acc
		_ = cfg.Save()
	}

	modelID, repo, file, ctx := cfg.GetActiveModelSpec()
	opts := worker.BootOptions{
		ModelID:     modelID,
		HFRepo:      repo,
		HFFile:      file,
		ContextSize: ctx,
	}

	fmt.Printf("⏳ Allocating Google Colab %s GPU for %s...\n", acc, modelID)
	rt, err := provider.SpinUpColabRuntimeHeadless(acc, worker.GenerateBootScript(opts))
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  %v\n", err)
		os.Exit(1)
	}
	cfg.UpdateTelemetry(config.WorkerTelemetry{
		RuntimeName:  rt.Name,
		JupyterURL:   rt.ConnectionInfo.URL,
		ProxyToken:   rt.ConnectionInfo.Token,
		Status:       "booting",
		StatusDetail: fmt.Sprintf("%s allocated // loading %s", acc, modelID),
		ActiveModel:  modelID,
	})
	fmt.Printf("✅ Colab %s runtime ready (%s)\n", acc, rt.Name)
}

func runColabDown() {
	count, err := provider.DeleteColabRuntimes("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  %v\n", err)
		os.Exit(1)
	}
	if cfg, err := config.LoadOrCreate(); err == nil {
		cfg.Lock()
		cfg.ActiveWorker = nil
		cfg.Unlock()
		_ = cfg.Save()
	}
	fmt.Printf("🛑 Stopped %d Colab GPU runtime(s).\n", count)
}
