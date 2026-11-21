#!/usr/bin/env python3
"""
FreeGPU Direct Colab GPU Worker (Zero-Tunnel Architecture).
Runs inside a headless Google Colab GPU runtime allocated via `colaboratory.googleapis.com`:
  1. Writes live `nvidia-smi` hardware telemetry & status every 2s to `/tmp/freegpu_telemetry.json`
     (read directly by the local Mac app over Google's `r.colab.dev` `/api/contents` REST endpoint).
  2. Downloads official pre-built CUDA `llama-server` (llama.cpp) and the requested GGUF coding model.
  3. Launches `llama-server` on `127.0.0.1:8081` with `--parallel 1`, `q8_0` KV cache, `--jinja`, and `--flash-attn on`.
"""

import argparse
import http.client
import json
import os
import platform
import queue
import shutil
import signal
import subprocess
import threading
import time
import urllib.request

STATE = {
    "status": "booting",
    "status_detail": "Initializing Colab GPU worker...",
    "active_model": "",
    "hf_repo": "",
    "hf_file": "",
    "context_size": 32768,
    "start_time": time.time(),
    "llama_healthy": False,
    "logs": [],
}
STATE_LOCK = threading.Lock()
LLAMA_PROC = None
LLAMA_PORT = 8081
WORK_DIR = "/tmp/freegpu"
TELEMETRY_FILE = "/tmp/freegpu_telemetry.json"
REQ_FILE = "/tmp/freegpu_req.json"

# Unsloth portable CUDA 12 build includes mainline llama.cpp + Inkling-2-Mini looped-transformer & NVFP4 support
LLAMA_BUILD_TAG = "unsloth-b12588-mix-c0c3de8"
PREBUILT_LLAMA_CUDA_URL = (
    "https://github.com/unslothai/llama.cpp/releases/download/b12588-mix-c0c3de8/app-b12588-mix-c0c3de8-linux-x64-cuda12-portable.tar.gz"
)
PREBUILT_CUDART_URL = (
    "https://github.com/ggml-org/llama.cpp/releases/download/b12608/cudart-llama-b12608-bin-ubuntu-cuda-12.8-x64.tar.gz"
)


def log(msg: str):
    ts = time.strftime("%H:%M:%S")
    line = f"[{ts}] {msg}"
    print(line, flush=True)
    with STATE_LOCK:
        STATE["logs"].append(line)
        if len(STATE["logs"]) > 60:
            STATE["logs"] = STATE["logs"][-60:]
    write_telemetry_file()


def set_status(status: str, detail: str):
    with STATE_LOCK:
        STATE["status"] = status
        STATE["status_detail"] = detail
    log(f"{status.upper()}: {detail}")


def query_gpus():
    try:
        out = subprocess.check_output(
            [
                "nvidia-smi",
                "--query-gpu=index,name,memory.used,memory.total,utilization.gpu,temperature.gpu,power.draw",
                "--format=csv,noheader,nounits",
            ],
            stderr=subprocess.DEVNULL,
            text=True,
            timeout=4,
        )
        gpus = []
        for line in out.strip().splitlines():
            parts = [p.strip() for p in line.split(",")]
            if len(parts) >= 6:
                def to_float(v):
                    try:
                        return float(v)
                    except Exception:
                        return 0.0

                gpus.append(
                    {
                        "index": int(parts[0]) if parts[0].isdigit() else 0,
                        "name": parts[1],
                        "vram_used_mb": to_float(parts[2]),
                        "vram_total_mb": to_float(parts[3]),
                        "util_percent": to_float(parts[4]),
                        "temp_c": to_float(parts[5]),
                        "power_watts": to_float(parts[6]) if len(parts) > 6 else 0.0,
                    }
                )
        return gpus
    except Exception:
        return []


def check_llama_health() -> bool:
    try:
        conn = http.client.HTTPConnection("127.0.0.1", LLAMA_PORT, timeout=1.5)
        conn.request("GET", "/health")
        resp = conn.getresponse()
        body = resp.read()
        conn.close()
        if resp.status == 200:
            data = json.loads(body.decode("utf-8", errors="ignore"))
            return data.get("status") == "ok"
        return False
    except Exception:
        return False


def write_telemetry_file():
    gpus = query_gpus()
    healthy = check_llama_health()
    with STATE_LOCK:
        STATE["llama_healthy"] = healthy
        if healthy and STATE["status"] == "loading_model":
            STATE["status"] = "ready"
            STATE["status_detail"] = f"READY // {STATE['active_model']}"
        payload = {
            "status": STATE["status"],
            "status_detail": STATE["status_detail"],
            "active_model": STATE["active_model"],
            "hf_repo": STATE["hf_repo"],
            "hf_file": STATE["hf_file"],
            "context_size": STATE["context_size"],
            "gpus": gpus,
            "uptime_seconds": round(time.time() - STATE["start_time"], 1),
            "llama_healthy": healthy,
            "recent_logs": list(STATE["logs"][-25:]),
        }
    try:
        tmp = TELEMETRY_FILE + ".tmp"
        with open(tmp, "w") as f:
            json.dump(payload, f)
        os.replace(tmp, TELEMETRY_FILE)
    except Exception:
        pass


def telemetry_loop():
    while True:
        write_telemetry_file()
        time.sleep(2.0)


REQ_QUEUE = queue.Queue()


def _handle_single_request(req_obj: dict):
    req_id = str(req_obj.get("id", ""))
    if not req_id or not all(c.isalnum() or c in "_-" for c in req_id) or len(req_id) > 64:
        return
    method = str(req_obj.get("method", "POST")).upper()
    if method not in ("GET", "POST"):
        return
    path = str(req_obj.get("path", "/v1/chat/completions"))
    if not path.startswith("/v1/") or ".." in path:
        return
    body_str = req_obj.get("body", "")
    resp_file = f"/tmp/freegpu_resp_{req_id}.txt"

    try:
        conn = http.client.HTTPConnection("127.0.0.1", LLAMA_PORT, timeout=600)
        body_bytes = body_str.encode("utf-8") if body_str else None
        conn.request(method, path, body=body_bytes, headers={"Content-Type": "application/json"})
        resp = conn.getresponse()
        if resp.status >= 400:
            err_body = resp.read().decode("utf-8", errors="replace")
            log(f"llama-server HTTP {resp.status}: {err_body[:240]}")
            with open(resp_file, "wb") as f:
                f.write(f"__FREEGPU_ERR__:{resp.status}:{err_body}\n__FREEGPU_EOF__".encode("utf-8"))
                f.flush()
            conn.close()
            return

        with open(resp_file, "wb") as f:
            while True:
                line = resp.readline()
                if not line:
                    break
                f.write(line)
                f.flush()
            f.write(b"\n__FREEGPU_EOF__")
            f.flush()
        conn.close()
    except Exception as e:
        log(f"bridge error ({req_id}): {e}")
        err_json = json.dumps({"error": {"message": str(e)}}).encode("utf-8")
        with open(resp_file, "wb") as f:
            f.write(b"__FREEGPU_ERR__:502:" + err_json + b"\n__FREEGPU_EOF__")
            f.flush()


def _queue_worker_loop():
    while True:
        req_obj = REQ_QUEUE.get()
        try:
            _handle_single_request(req_obj)
        except Exception:
            pass
        finally:
            REQ_QUEUE.task_done()


def request_bridge_loop():
    # Match --parallel 2 so concurrent OpenCode requests (chat + background title/summary) don't block each other
    for _ in range(2):
        threading.Thread(target=_queue_worker_loop, daemon=True).start()
    seen_ids = set()
    while True:
        try:
            entries = sorted(f for f in os.listdir("/tmp") if f.startswith("freegpu_req"))
            for fname in entries:
                fpath = os.path.join("/tmp", fname)
                try:
                    with open(fpath, "r") as f:
                        raw = f.read()
                    if not raw:
                        continue
                    req_obj = json.loads(raw)
                    req_id = req_obj.get("id", "")
                    try:
                        os.remove(fpath)
                    except Exception:
                        pass
                    if req_id and req_id not in seen_ids:
                        seen_ids.add(req_id)
                        if len(seen_ids) > 500:
                            seen_ids.clear()
                        REQ_QUEUE.put(req_obj)
                except Exception:
                    continue
        except Exception:
            pass
        time.sleep(0.04)


def _test_llama_bin(llama_bin: str, bin_dir: str) -> bool:
    if not (os.path.exists(llama_bin) and os.access(llama_bin, os.X_OK)):
        return False
    env = os.environ.copy()
    env["LD_LIBRARY_PATH"] = f"{bin_dir}:/usr/local/cuda/lib64:" + env.get("LD_LIBRARY_PATH", "")
    try:
        res = subprocess.run(
            [llama_bin, "--list-devices"],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            env=env,
            timeout=10,
            text=True,
        )
        if res.returncode != 0:
            return False
        if query_gpus() and "CUDA" not in res.stdout:
            return False
        return True
    except Exception:
        return False


def ensure_llama_server() -> str:
    bin_dir = os.path.join(WORK_DIR, "bin")
    os.makedirs(bin_dir, exist_ok=True)
    llama_bin = os.path.join(bin_dir, "llama-server")
    ver_file = os.path.join(bin_dir, ".version")

    cached_ver = ""
    if os.path.exists(ver_file):
        try:
            cached_ver = open(ver_file).read().strip()
        except Exception:
            pass

    if cached_ver == LLAMA_BUILD_TAG and _test_llama_bin(llama_bin, bin_dir):
        log(f"Using cached CUDA llama-server ({LLAMA_BUILD_TAG})")
        return llama_bin

    if platform.system() == "Linux" and platform.machine() in ("x86_64", "amd64"):
        try:
            shutil.rmtree(bin_dir, ignore_errors=True)
            os.makedirs(bin_dir, exist_ok=True)
            tar_path = os.path.join(WORK_DIR, "llama-cuda.tar.gz")
            log(f"Fetching pre-built CUDA llama-server ({LLAMA_BUILD_TAG})...")
            urllib.request.urlretrieve(PREBUILT_LLAMA_CUDA_URL, tar_path)
            subprocess.run(["tar", "-xzf", tar_path, "-C", bin_dir], check=True)
            os.remove(tar_path)
            if not os.path.exists(llama_bin):
                # Handle archives that wrap binaries in a single top-level directory
                for root_d, _, files in os.walk(bin_dir):
                    if "llama-server" in files:
                        for fn in os.listdir(root_d):
                            src = os.path.join(root_d, fn)
                            dst = os.path.join(bin_dir, fn)
                            if src != dst and not os.path.exists(dst):
                                shutil.move(src, dst)
                        break
            os.chmod(llama_bin, 0o755)

            if _test_llama_bin(llama_bin, bin_dir):
                open(ver_file, "w").write(LLAMA_BUILD_TAG)
                log(f"CUDA llama-server ({LLAMA_BUILD_TAG}) verified!")
                return llama_bin

            log("Fetching CUDA 12.8 runtime bundle (libcublas)...")
            cudart_tar = os.path.join(WORK_DIR, "cudart.tar.gz")
            urllib.request.urlretrieve(PREBUILT_CUDART_URL, cudart_tar)
            subprocess.run(["tar", "-xzf", cudart_tar, "--strip-components=1", "-C", bin_dir], check=False)
            if not _test_llama_bin(llama_bin, bin_dir):
                subprocess.run(["tar", "-xzf", cudart_tar, "-C", bin_dir], check=False)
            if os.path.exists(cudart_tar):
                os.remove(cudart_tar)

            if _test_llama_bin(llama_bin, bin_dir):
                open(ver_file, "w").write(LLAMA_BUILD_TAG)
                log(f"CUDA llama-server ({LLAMA_BUILD_TAG}) + runtime bundle ready!")
                return llama_bin
        except Exception as e:
            log(f"Pre-built binary issue ({e})")

    raise RuntimeError("Failed to prepare CUDA llama-server")


def download_gguf_model(hf_repo: str, hf_file: str) -> str:
    models_dir = os.path.join(WORK_DIR, "models")
    os.makedirs(models_dir, exist_ok=True)
    dest_path = os.path.join(models_dir, os.path.basename(hf_file))

    if os.path.exists(dest_path) and os.path.getsize(dest_path) > 100 * 1024 * 1024:
        log(f"Model cached on disk: {os.path.basename(hf_file)}")
        return dest_path

    url = f"https://huggingface.co/{hf_repo}/resolve/main/{hf_file}?download=true"
    log(f"Downloading {hf_file} (8x parallel streams)...")

    tmp_path = dest_path + ".part"
    head_req = urllib.request.Request(url, method="HEAD", headers={"User-Agent": "FreeGPU-Worker/1.0"})
    total = 0
    accept_ranges = False
    try:
        with urllib.request.urlopen(head_req, timeout=30) as hresp:
            total = int(hresp.headers.get("Content-Length", "0") or "0")
            accept_ranges = "bytes" in (hresp.headers.get("Accept-Ranges", "") or "").lower()
    except Exception:
        pass

    # Use 8-thread parallel HTTP Range downloader when supported (3-4x faster on Colab!)
    if total > 50 * 1024 * 1024 and accept_ranges:
        with open(tmp_path, "wb") as f:
            f.truncate(total)

        num_workers = 8
        chunk_size = total // num_workers
        progress = {"done": 0, "err": None}
        prog_lock = threading.Lock()
        start_dl = time.time()

        def worker_range(idx: int, start_b: int, end_b: int):
            try:
                r = urllib.request.Request(
                    url,
                    headers={"User-Agent": "FreeGPU-Worker/1.0", "Range": f"bytes={start_b}-{end_b}"},
                )
                with urllib.request.urlopen(r, timeout=120) as resp:
                    with open(tmp_path, "r+b") as out_f:
                        out_f.seek(start_b)
                        while True:
                            buf = resp.read(4 * 1024 * 1024)
                            if not buf:
                                break
                            out_f.write(buf)
                            with prog_lock:
                                progress["done"] += len(buf)
            except Exception as e:
                with prog_lock:
                    progress["err"] = e

        threads = []
        for i in range(num_workers):
            s_b = i * chunk_size
            e_b = (total - 1) if i == num_workers - 1 else (s_b + chunk_size - 1)
            t = threading.Thread(target=worker_range, args=(i, s_b, e_b), daemon=True)
            t.start()
            threads.append(t)

        while any(t.is_alive() for t in threads):
            time.sleep(2.0)
            with prog_lock:
                done_b = progress["done"]
            mb_done = done_b / (1024 * 1024)
            mb_total = total / (1024 * 1024)
            speed = mb_done / max(0.1, time.time() - start_dl)
            pct = min(99.9, done_b * 100.0 / total)
            set_status("downloading", f"DL {pct:.0f}% ({mb_done:.0f}/{mb_total:.0f} MB @ {speed:.0f} MB/s)")

        for t in threads:
            t.join()

        if progress["err"] is None:
            os.replace(tmp_path, dest_path)
            log(f"Download complete: {os.path.basename(hf_file)}")
            return dest_path
        log(f"Parallel range fallback ({progress['err']}), retrying single stream...")

    # Single-stream fallback
    req = urllib.request.Request(url, headers={"User-Agent": "FreeGPU-Worker/1.0"})
    with urllib.request.urlopen(req, timeout=60) as resp:
        total = int(resp.headers.get("Content-Length", "0") or "0")
        downloaded = 0
        last_report = 0.0
        start_dl = time.time()
        with open(tmp_path, "wb") as f:
            while True:
                chunk = resp.read(8 * 1024 * 1024)
                if not chunk:
                    break
                f.write(chunk)
                downloaded += len(chunk)
                now = time.time()
                if now - last_report >= 2.0:
                    last_report = now
                    mb_done = downloaded / (1024 * 1024)
                    mb_total = total / (1024 * 1024) if total else 0
                    speed = mb_done / max(0.1, now - start_dl)
                    pct = (downloaded * 100.0 / total) if total else 0
                    set_status("downloading", f"DL {pct:.0f}% ({mb_done:.0f}/{mb_total:.0f} MB @ {speed:.0f} MB/s)")

    os.replace(tmp_path, dest_path)
    log(f"Download complete: {os.path.basename(hf_file)}")
    return dest_path


def start_llama(hf_repo: str, hf_file: str, context_size: int, model_alias: str):
    global LLAMA_PROC

    results = {"llama_bin": None, "model_path": None, "error": None}

    def task_bin():
        try:
            results["llama_bin"] = ensure_llama_server()
        except Exception as e:
            results["error"] = str(e)

    def task_dl():
        try:
            results["model_path"] = download_gguf_model(hf_repo, hf_file)
        except Exception as e:
            results["error"] = str(e)

    set_status("downloading", f"Fetching {hf_file}...")
    t1 = threading.Thread(target=task_bin, daemon=True)
    t2 = threading.Thread(target=task_dl, daemon=True)
    t1.start()
    t2.start()
    t1.join()
    t2.join()

    if results["error"] or not results["llama_bin"] or not results["model_path"]:
        set_status("error", results["error"] or "Failed to load binary/model")
        return

    llama_bin = results["llama_bin"]
    model_path = results["model_path"]
    model_size_gb = os.path.getsize(model_path) / (1024 ** 3) if os.path.exists(model_path) else 20.0
    gpus = query_gpus()
    total_vram_mb = sum(g.get("vram_total_mb", 0) for g in gpus)

    cmd = [
        llama_bin,
        "--model",
        model_path,
        "--alias",
        model_alias,
        "--host",
        "127.0.0.1",
        "--port",
        str(LLAMA_PORT),
        "--jinja",
        "--flash-attn",
        "on",
        "--cache-reuse",
        "256",
        "--slot-prompt-similarity",
        "0.5",
        "--metrics",
    ]

    if total_vram_mb >= 70000:
        # 80GB A100: Full 128k context + 2 unified KV slots (so OpenCode title calls never evict your main chat cache!)
        target_ctx = max(context_size, 131072)
        if "qwen2.5" in hf_file.lower():
            target_ctx = 32768
        cmd.extend([
            "--ctx-size", str(target_ctx),
            "--parallel", "2",
            "--kv-unified",
            "--n-gpu-layers", "99",
            "--batch-size", "4096",
            "--ubatch-size", "2048",
        ])
        if model_size_gb >= 32.0:
            # 80B MoE (43GB): q8_0 KV keeps 128k + 2 slots well inside 80GB VRAM
            cmd.extend(["--cache-type-k", "q8_0", "--cache-type-v", "q8_0"])
    elif total_vram_mb >= 35000:
        # 40GB A100: 64k context + q8_0 KV + large A100 Tensor Core batches
        target_ctx = min(max(context_size, 65536), 65536)
        if "qwen2.5" in hf_file.lower():
            target_ctx = 32768
        cmd.extend([
            "--ctx-size", str(target_ctx),
            "--parallel", "1",
            "--n-gpu-layers", "99",
            "--batch-size", "4096",
            "--ubatch-size", "2048",
            "--cache-type-k", "q8_0",
            "--cache-type-v", "q8_0",
        ])
    else:
        # 16GB T4 / 24GB L4: smart VRAM fit so all layers stay in VRAM with zero CPU spill
        kv_type = "q8_0"
        target_ctx = min(context_size, 32768)
        name_l = (hf_file + " " + model_alias).lower()
        if 0 < total_vram_mb <= 25000:
            if any(k in name_l for k in ("35b", "30b", "a3b", "glm-4.7")):
                # MoE / hybrid-attention 30B-35B models have tiny KV cache; 32k fits cleanly in 24GB L4 with q4_0 KV
                target_ctx = min(target_ctx, 32768)
                kv_type = "q4_0"
            elif "32b" in name_l:
                # Dense 32B needs slightly tighter context on 24GB L4
                target_ctx = min(target_ctx, 24576)
                kv_type = "q4_0"
        cmd.extend([
            "--ctx-size", str(target_ctx),
            "--parallel", "1",
            "--cache-type-k", kv_type,
            "--cache-type-v", kv_type,
        ])

    env = os.environ.copy()
    bin_dir = os.path.dirname(llama_bin)
    env["LD_LIBRARY_PATH"] = f"{bin_dir}:/usr/local/cuda/lib64:" + env.get("LD_LIBRARY_PATH", "")

    set_status("loading_model", f"Loading {model_alias} into VRAM...")
    LLAMA_PROC = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        env=env,
    )

    def pump(proc):
        for line in proc.stdout:
            s = line.strip()
            if s and ("load_model" in s or "server_on_li" in s or "error" in s.lower() or "slot" in s):
                log(s)

    threading.Thread(target=pump, args=(LLAMA_PROC,), daemon=True).start()

    for _ in range(180):
        if LLAMA_PROC.poll() is not None:
            set_status("error", f"llama-server exited ({LLAMA_PROC.returncode})")
            return
        if check_llama_health():
            set_status("loading_model", "Warming up CUDA kernels (1-token pre-JIT)...")
            try:
                conn = http.client.HTTPConnection("127.0.0.1", LLAMA_PORT, timeout=120)
                warmup_body = json.dumps({
                    "model": model_alias,
                    "messages": [{"role": "user", "content": "hi"}],
                    "max_tokens": 1,
                }).encode("utf-8")
                conn.request("POST", "/v1/chat/completions", body=warmup_body, headers={"Content-Type": "application/json"})
                conn.getresponse().read()
                conn.close()
            except Exception:
                pass
            set_status("ready", f"READY // {model_alias}")
            return
        time.sleep(1.0)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--hf-repo", default=os.environ.get("FREEGPU_HF_REPO", "bartowski/Qwen2.5-Coder-32B-Instruct-GGUF"))
    parser.add_argument("--hf-file", default=os.environ.get("FREEGPU_HF_FILE", "Qwen2.5-Coder-32B-Instruct-Q4_K_M.gguf"))
    parser.add_argument("--model-id", default=os.environ.get("FREEGPU_MODEL_ID", "qwen2.5-coder-32b"))
    parser.add_argument("--ctx-size", type=int, default=int(os.environ.get("FREEGPU_CTX_SIZE", "32768")))
    args = parser.parse_args()

    os.makedirs(WORK_DIR, exist_ok=True)

    # Terminate any previous freegpu_boot / llama-server process safely via /proc
    my_pid = os.getpid()
    if os.path.exists("/proc"):
        for entry in os.listdir("/proc"):
            if entry.isdigit() and int(entry) != my_pid:
                try:
                    cmdline = open(f"/proc/{entry}/cmdline", "rb").read().decode("utf-8", errors="ignore")
                    if "freegpu_boot" in cmdline or "llama-server" in cmdline:
                        os.kill(int(entry), signal.SIGKILL)
                except Exception:
                    pass
    time.sleep(0.3)

    with STATE_LOCK:
        STATE["hf_repo"] = args.hf_repo
        STATE["hf_file"] = args.hf_file
        STATE["active_model"] = args.model_id
        STATE["context_size"] = args.ctx_size

    gpus = query_gpus()
    if gpus:
        log(f"GPU: {gpus[0]['name']} ({gpus[0]['vram_total_mb']:.0f} MB VRAM)")

    threading.Thread(target=telemetry_loop, daemon=True).start()
    threading.Thread(target=request_bridge_loop, daemon=True).start()
    start_llama(args.hf_repo, args.hf_file, args.ctx_size, args.model_id)

    while True:
        time.sleep(10)


if __name__ == "__main__":
    main()
