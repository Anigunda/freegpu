# ⚡ FreeGPU — Colab GPU Terminal for OpenCode

**Turn Google Colab GPUs (80 GB A100, 24 GB L4, 16 GB T4) into a native macOS desktop terminal and static local OpenAI endpoint (`http://127.0.0.1:11435/v1`) for [OpenCode](https://opencode.ai) — with zero tunnels and zero notebook uploads.**

<p align="center">
  <video src="https://github.com/Anigunda/freegpu/raw/main/assets/freegpu-demo.mp4" controls width="100%"></video>
</p>

> Can't see the inline player? [Watch the launch demo directly](https://github.com/Anigunda/freegpu/raw/main/assets/freegpu-demo.mp4).

- **Direct Google Colab REST API (Zero Tunnels):** Allocates headless Colab GPUs directly via `colaboratory.googleapis.com` and streams OpenAI requests over persistent HTTP/2 — no `cloudflared`, no ngrok, no browser tabs.
- **Native macOS Desktop App (`FreeGPU.app`):** Split-screen GPU telemetry + live Colab Compute Units balance + embedded interactive PTY terminal (`opencode`) with **Focus Mode (`Cmd+Shift+F`)** and inline live stats (`tok/s`, VRAM, GPU temp, CU).
- **Pre-compiled CUDA 12.8 `llama-server` + 128k Context:** Auto-tunes for 80 GB A100 (`--ctx-size 131072 --parallel 2 --kv-unified --flash-attn on`) and auto-fits 24 GB L4 / 16 GB T4 with quantized KV cache.
- **Zero-Config OpenCode Integration:** Automatically configures `~/.config/opencode/opencode.jsonc`, `config.json`, and `auth.json` on launch.
- **Native Go OAuth2 PKCE + Pure-Go ADC Refresh:** Supports 1-click Google Sign-In directly inside the app (no `gcloud` CLI required) with automatic fallback to existing `gcloud` credentials if present.

---

## 🚀 Install

### Option 1: macOS `.dmg` (Recommended)
1. Download `FreeGPU-v1.0.0-arm64.dmg` from Releases.
2. Drag **FreeGPU.app** into **Applications** and open it.
3. Click **SIGN IN** (if prompted), pick your GPU & model, and click **▶ START GPU**.

### Option 2: Homebrew Cask
```bash
brew install --cask packaging/homebrew/freegpu.rb
```

### Option 3: Build from Source
```bash
git clone https://github.com/Anigunda/freegpu.git
cd freegpu
./install.sh
open FreeGPU.app
```

---

## 📦 Curated Coding Models (`llama.cpp` GGUF)

| Preset ID | Model | Size | Default Context | Best GPU Fit |
| :--- | :--- | :--- | :--- | :--- |
| `qwen3.6-35b-a3b` *(default)* | Qwen 3.6 35B | 20.6 GB | 128k | A100 (80GB) / L4 (24GB) |
| `qwen3-coder-next` | Qwen 3 Coder Next 80B | 42.9 GB | 128k | A100 (80GB) |
| `qwen3-coder-30b-a3b` | Qwen 3 Coder 30B | 17.3 GB | 128k | A100 / L4 (24GB) |
| `nemotron-3.5-30b-a3b` | Nemotron 3.5 30B | 21.6 GB | 128k | A100 (80GB) / L4 (24GB) |
| `glm-4.7-flash` | GLM 4.7 Flash 30B | 17.1 GB | 128k | A100 / L4 (24GB) |
| `gpt-oss-20b` | gpt-oss 20B | 10.8 GB | 128k | T4 (16GB) / L4 / A100 |
| `devstral-small-2-24b` | Devstral Small 2 24B | 13.3 GB | 128k | L4 (24GB) / A100 (80GB) |
| `qwen2.5-coder-32b` | Qwen 2.5 Coder 32B | 19.8 GB | 32k | A100 (80GB) / L4 (24GB) |

---

## 🔐 Authentication (Zero `gcloud` Required)

- **Native Google OAuth2 (PKCE):** Place a Google Cloud Desktop OAuth JSON at `~/.freegpu/oauth_client.json` (or build a release with `make dmg OAUTH_CLIENT_ID=... OAUTH_CLIENT_SECRET=...`) and click **SIGN IN** inside `FreeGPU.app`.
- **Existing `gcloud` ADC:** If `~/.config/gcloud/application_default_credentials.json` is already present, FreeGPU refreshes tokens directly in pure Go with zero `gcloud` subprocess overhead.

---

## 🛠 Packaging & CLI

```bash
make test         # Run go vet + Python syntax check
make app          # Build FreeGPU.app bundle
make dmg          # Build distributable dist/FreeGPU-v1.0.0-<arch>.dmg
```

```text
freegpu                    Launch native macOS Desktop App
freegpu serve              Run headless HTTP gateway (127.0.0.1:11435)
freegpu colab-up           Allocate Colab GPU & boot llama-server
freegpu colab-down         Terminate active Colab GPU runtime
freegpu opencode           Sync FreeGPU provider into OpenCode config
```
