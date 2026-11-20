package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/anilgoudgunda/freegpu/internal/auth"
	"github.com/anilgoudgunda/freegpu/internal/config"
)

const colabAPIBase = "https://colaboratory.googleapis.com/v1beta"

// Shared HTTP client with connection pooling for low-latency Colab REST polling
var colabHTTPClient = &http.Client{
	Timeout: 45 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
}

type ColabCCUInfo struct {
	CurrentBalance        float64  `json:"currentBalance"`
	ConsumptionRateHourly float64  `json:"consumptionRateHourly"`
	AssignmentsCount      int      `json:"assignmentsCount"`
	EligibleGPUs          []string `json:"eligibleGpus"`
}

// FetchColabCCUInfo fetches the user's real live Compute Units balance and hourly burn rate from Google Colab.
func FetchColabCCUInfo() (*ColabCCUInfo, error) {
	token, _, err := GetGcloudTokenAndProject()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("GET", "https://colab.research.google.com/tun/m/ccu-info?authuser=0", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Colab-Tunnel", "Google")
	req.Header.Set("Accept", "application/json")

	resp, err := colabHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ccu-info HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Strip Google XSSI prefix ")]}'\n"
	trimmed := strings.TrimSpace(string(body))
	trimmed = strings.TrimPrefix(trimmed, ")]}'")
	trimmed = strings.TrimSpace(trimmed)

	var info ColabCCUInfo
	if err := json.Unmarshal([]byte(trimmed), &info); err != nil {
		return nil, err
	}
	return &info, nil
}

type ColabRuntimeSpec struct {
	Key struct {
		Variant     string `json:"variant"`
		Accelerator string `json:"accelerator"`
		Shape       string `json:"shape"`
	} `json:"key"`
	Eligible bool `json:"eligible"`
}

type ColabRuntime struct {
	Name        string `json:"name"`
	RuntimeSpec struct {
		Variant     string `json:"variant"`
		Accelerator string `json:"accelerator"`
		Shape       string `json:"shape"`
	} `json:"runtimeSpec"`
	ConnectionInfo struct {
		URL        string `json:"url"`
		Token      string `json:"token"`
		ExpireTime string `json:"expireTime"`
	} `json:"connectionInfo"`
}

type ColabAPIStatus struct {
	GcloudFound      bool           `json:"gcloud_found"`
	ProjectID        string         `json:"project_id"`
	Authorized       bool           `json:"authorized"`
	NeedsScopeAuth   bool           `json:"needs_scope_auth"`
	SubscriptionTier string         `json:"subscription_tier"`
	EligibleGPUs     []string       `json:"eligible_gpus"`
	ActiveRuntimes   []ColabRuntime `json:"active_runtimes"`
	ErrorMessage     string         `json:"error_message,omitempty"`
}

func findGcloudBin() string {
	if p, err := exec.LookPath("gcloud"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, c := range []string{
		filepath.Join(home, "google-cloud-sdk", "bin", "gcloud"),
		"/opt/homebrew/bin/gcloud",
		"/usr/local/bin/gcloud",
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func GetGcloudTokenAndProject() (token, project string, err error) {
	// 1. Prefer native Go OAuth2 token or pure-Go ADC refresh (no gcloud CLI required)
	if tok, proj, _, err := auth.GetAccessToken(); err == nil && tok != "" {
		return tok, proj, nil
	}

	// 2. Fallback to gcloud CLI if installed
	gcloud := findGcloudBin()
	if gcloud == "" {
		return "", "", fmt.Errorf("not signed in to Google (click SIGN IN WITH GOOGLE or run gcloud auth)")
	}
	projOut, _ := exec.Command(gcloud, "config", "get-value", "project").Output()
	project = strings.TrimSpace(string(projOut))

	tokOut, err := exec.Command(gcloud, "auth", "application-default", "print-access-token").Output()
	if err != nil || len(strings.TrimSpace(string(tokOut))) == 0 {
		tokOut, err = exec.Command(gcloud, "auth", "print-access-token").Output()
		if err != nil {
			return "", project, fmt.Errorf("gcloud token failed: %w", err)
		}
	}
	return strings.TrimSpace(string(tokOut)), project, nil
}

func colabRequest(method, urlStr, token, project string, body any) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, urlStr, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if project != "" {
		req.Header.Set("x-goog-user-project", project)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

func InspectColabAPI() ColabAPIStatus {
	gcloud := findGcloudBin()
	st := ColabAPIStatus{GcloudFound: gcloud != ""}

	token, project, err := GetGcloudTokenAndProject()
	st.ProjectID = project
	if err != nil {
		st.NeedsScopeAuth = true
		st.ErrorMessage = err.Error()
		return st
	}

	specBytes, code, err := colabRequest("GET", colabAPIBase+"/runtimespecs", token, project, nil)
	if err != nil {
		st.ErrorMessage = err.Error()
		return st
	}
	if code == 403 || code == 401 {
		st.NeedsScopeAuth = true
		st.ErrorMessage = "Colab OAuth scope required"
		return st
	}

	st.Authorized = true
	var specsResp struct {
		RuntimeSpecs []ColabRuntimeSpec `json:"runtimeSpecs"`
	}
	if json.Unmarshal(specBytes, &specsResp) == nil {
		seen := make(map[string]bool)
		for _, sp := range specsResp.RuntimeSpecs {
			if sp.Eligible && sp.Key.Variant == "VARIANT_GPU" && sp.Key.Accelerator != "" && !seen[sp.Key.Accelerator] {
				st.EligibleGPUs = append(st.EligibleGPUs, sp.Key.Accelerator)
				seen[sp.Key.Accelerator] = true
			}
		}
	}

	subBytes, subCode, _ := colabRequest("GET", colabAPIBase+"/subscription", token, project, nil)
	if subCode == 200 {
		var sub struct {
			Tier string `json:"tier"`
		}
		if json.Unmarshal(subBytes, &sub) == nil {
			st.SubscriptionTier = sub.Tier
		}
	}

	rtBytes, rtCode, _ := colabRequest("GET", colabAPIBase+"/runtimes", token, project, nil)
	if rtCode == 200 {
		var rtResp struct {
			Runtimes []ColabRuntime `json:"runtimes"`
		}
		if json.Unmarshal(rtBytes, &rtResp) == nil {
			for i := range rtResp.Runtimes {
				// Populate connectionInfo for active runtimes
				fullBytes, fCode, _ := colabRequest("GET", colabAPIBase+"/"+rtResp.Runtimes[i].Name, token, project, nil)
				if fCode == 200 {
					var fullRT ColabRuntime
					if json.Unmarshal(fullBytes, &fullRT) == nil {
						rtResp.Runtimes[i] = fullRT
					}
				}
			}
			st.ActiveRuntimes = rtResp.Runtimes
		}
	}
	return st
}

func openBrowserURL(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

func TriggerColabOAuthLogin(ports ...int) error {
	port := 11435
	if len(ports) > 0 && ports[0] > 0 {
		port = ports[0]
	}

	// 1. Prefer native Go OAuth2 PKCE flow (zero gcloud CLI needed)
	if auth.HasNativeClient() {
		authURL, err := auth.StartLogin(port)
		if err != nil {
			return err
		}
		return openBrowserURL(authURL)
	}

	// 2. Fallback to gcloud CLI if installed
	gcloud := findGcloudBin()
	if gcloud == "" {
		return fmt.Errorf("no native OAuth client (~/.freegpu/oauth_client.json) and gcloud CLI not found")
	}
	_, project, _ := GetGcloudTokenAndProject()
	if project != "" {
		_ = exec.Command(gcloud, "services", "enable", "colaboratory.googleapis.com", "--project="+project).Run()
	}
	cmd := exec.Command(
		gcloud,
		"auth",
		"application-default",
		"login",
		"--scopes=openid,https://www.googleapis.com/auth/cloud-platform,https://www.googleapis.com/auth/userinfo.email,https://www.googleapis.com/auth/colaboratory",
	)
	return cmd.Start()
}

func SpinUpColabRuntimeHeadless(accelerator string, bootScript string) (*ColabRuntime, error) {
	token, project, err := GetGcloudTokenAndProject()
	if err != nil {
		return nil, err
	}
	if accelerator == "" {
		accelerator = "A100"
	}

	var targetRuntime *ColabRuntime
	rtBytes, rtCode, _ := colabRequest("GET", colabAPIBase+"/runtimes", token, project, nil)
	if rtCode == 200 {
		var rtResp struct {
			Runtimes []ColabRuntime `json:"runtimes"`
		}
		if json.Unmarshal(rtBytes, &rtResp) == nil {
			for i := range rtResp.Runtimes {
				if rtResp.Runtimes[i].RuntimeSpec.Variant == "VARIANT_GPU" {
					// If user requested a different GPU accelerator than the running one, replace it
					if strings.EqualFold(rtResp.Runtimes[i].RuntimeSpec.Accelerator, accelerator) {
						targetRuntime = &rtResp.Runtimes[i]
						break
					} else {
						_, _, _ = colabRequest("DELETE", colabAPIBase+"/"+rtResp.Runtimes[i].Name, token, project, nil)
					}
				}
			}
		}
	}

	if targetRuntime == nil {
		tryAllocate := func(acc, shape string, waitSec int) *ColabRuntime {
			createPayload := map[string]any{
				"runtimeSpec": map[string]any{
					"variant":     "VARIANT_GPU",
					"accelerator": strings.ToUpper(acc),
					"shape":       shape,
				},
			}
			respBytes, code, err := colabRequest("POST", colabAPIBase+"/runtimes", token, project, createPayload)
			if err != nil || code >= 400 {
				return nil
			}
			var op struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(respBytes, &op)

			deadline := time.Now().Add(time.Duration(waitSec) * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(2 * time.Second)

				// Poll /v1/operations/{id} to detect NO_RUNTIMES stockout immediately (~8-10s)
				if op.Name != "" {
					opBytes, opCode, _ := colabRequest("GET", "https://colaboratory.googleapis.com/v1/"+op.Name, token, project, nil)
					if opCode == 200 {
						var opState struct {
							Done  bool `json:"done"`
							Error *struct {
								Code    int    `json:"code"`
								Message string `json:"message"`
							} `json:"error"`
						}
						if json.Unmarshal(opBytes, &opState) == nil && opState.Done && opState.Error != nil {
							return nil
						}
					}
				}

				listBytes, lCode, _ := colabRequest("GET", colabAPIBase+"/runtimes", token, project, nil)
				if lCode == 200 {
					var rtResp struct {
						Runtimes []ColabRuntime `json:"runtimes"`
					}
					if json.Unmarshal(listBytes, &rtResp) == nil && len(rtResp.Runtimes) > 0 {
						rt := rtResp.Runtimes[len(rtResp.Runtimes)-1]
						if rt.ConnectionInfo.URL != "" {
							return &rt
						}
						fullBytes, fCode, _ := colabRequest("GET", colabAPIBase+"/"+rt.Name, token, project, nil)
						if fCode == 200 {
							var fullRT ColabRuntime
							if json.Unmarshal(fullBytes, &fullRT) == nil && fullRT.ConnectionInfo.URL != "" {
								return &fullRT
							}
						}
					}
				}
			}
			return nil
		}

		shape := "SHAPE_STANDARD"
		if strings.EqualFold(accelerator, "A100") {
			shape = "SHAPE_HIGHMEM"
		}
		targetRuntime = tryAllocate(accelerator, shape, 45)
		if targetRuntime == nil && strings.EqualFold(accelerator, "A100") {
			// Try A100 SHAPE_STANDARD, then G4, then L4 if Google Colab returns NO_RUNTIMES stockout for A100
			targetRuntime = tryAllocate("A100", "SHAPE_STANDARD", 30)
			if targetRuntime == nil {
				targetRuntime = tryAllocate("L4", "SHAPE_STANDARD", 30)
			}
		}
	} else if targetRuntime.ConnectionInfo.URL == "" {
		fullBytes, fCode, _ := colabRequest("GET", colabAPIBase+"/"+targetRuntime.Name, token, project, nil)
		if fCode == 200 {
			var fullRT ColabRuntime
			if json.Unmarshal(fullBytes, &fullRT) == nil && fullRT.ConnectionInfo.URL != "" {
				targetRuntime = &fullRT
			}
		}
	}

	if targetRuntime == nil || targetRuntime.ConnectionInfo.URL == "" {
		return nil, fmt.Errorf("timed out waiting for Colab GPU runtime")
	}

	if err := executeScriptOnColabJupyter(targetRuntime.ConnectionInfo.URL, targetRuntime.ConnectionInfo.Token, token, bootScript); err != nil {
		return targetRuntime, fmt.Errorf("runtime allocated (%s), bootstrap error: %w", targetRuntime.Name, err)
	}
	return targetRuntime, nil
}

func DeleteColabRuntimes(runtimeName string) (int, error) {
	token, project, err := GetGcloudTokenAndProject()
	if err != nil {
		return 0, err
	}

	var toDelete []string
	if runtimeName != "" {
		if !strings.HasPrefix(runtimeName, "runtimes/") {
			runtimeName = "runtimes/" + runtimeName
		}
		toDelete = append(toDelete, runtimeName)
	} else {
		rtBytes, rtCode, _ := colabRequest("GET", colabAPIBase+"/runtimes", token, project, nil)
		if rtCode == 200 {
			var rtResp struct {
				Runtimes []ColabRuntime `json:"runtimes"`
			}
			if json.Unmarshal(rtBytes, &rtResp) == nil {
				for _, r := range rtResp.Runtimes {
					toDelete = append(toDelete, r.Name)
				}
			}
		}
	}

	deleted := 0
	for _, name := range toDelete {
		_, code, err := colabRequest("DELETE", colabAPIBase+"/"+name, token, project, nil)
		if err == nil && code < 300 {
			deleted++
		}
	}
	return deleted, nil
}

func executeScriptOnColabJupyter(jupyterURL, proxyToken, oauthToken, bootScript string) error {
	baseURL := strings.TrimRight(jupyterURL, "/")
	client := &http.Client{Timeout: 20 * time.Second}

	putPayload, _ := json.Marshal(map[string]any{
		"type":    "file",
		"format":  "text",
		"content": bootScript,
	})

	var termName string
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("PUT", baseURL+"/api/contents/freegpu_boot.py", bytes.NewReader(putPayload))
		req.Header.Set("X-Colab-Runtime-Proxy-Token", proxyToken)
		req.Header.Set("Authorization", "Bearer "+oauthToken)
		req.Header.Set("Content-Type", "application/json")
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
		}

		tReq, _ := http.NewRequest("POST", baseURL+"/api/terminals", nil)
		tReq.Header.Set("X-Colab-Runtime-Proxy-Token", proxyToken)
		tReq.Header.Set("Authorization", "Bearer "+oauthToken)
		if tResp, err := client.Do(tReq); err == nil {
			tBytes, _ := io.ReadAll(tResp.Body)
			_ = tResp.Body.Close()
			var term struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(tBytes, &term) == nil && term.Name != "" {
				termName = term.Name
				break
			}
		}
		time.Sleep(2 * time.Second)
	}

	if termName == "" {
		return fmt.Errorf("timed out waiting for Colab Jupyter server")
	}

	wsURL := strings.Replace(baseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsTermURL := fmt.Sprintf("%s/terminals/websocket/%s", wsURL, termName)

	pyRunner := `
import sys, json, ssl, socket, base64, os, time
from urllib.parse import urlparse

ws_url, proxy_token, oauth_token = sys.argv[1], sys.argv[2], sys.argv[3]
u = urlparse(ws_url)
host = u.hostname
port = u.port or (443 if u.scheme == "wss" else 80)
path = u.path + ("?" + u.query if u.query else "")

raw_sock = socket.create_connection((host, port), timeout=15)
if u.scheme == "wss":
    ctx = ssl.create_default_context()
    sock = ctx.wrap_socket(raw_sock, server_hostname=host)
else:
    sock = raw_sock

key = base64.b64encode(os.urandom(16)).decode()
handshake = (
    f"GET {path} HTTP/1.1\r\n"
    f"Host: {host}\r\n"
    f"Origin: https://{host}\r\n"
    f"Upgrade: websocket\r\n"
    f"Connection: Upgrade\r\n"
    f"Sec-WebSocket-Key: {key}\r\n"
    f"Sec-WebSocket-Version: 13\r\n"
    f"X-Colab-Runtime-Proxy-Token: {proxy_token}\r\n"
    f"Authorization: Bearer {oauth_token}\r\n"
    f"\r\n"
)
sock.sendall(handshake.encode())
resp = b""
while b"\r\n\r\n" not in resp:
    chunk = sock.recv(4096)
    if not chunk:
        break
    resp += chunk

time.sleep(1.0)
cmd_str = "nohup python3 /freegpu_boot.py > /tmp/freegpu.log 2>&1 &\r\n"
payload = json.dumps(["stdin", cmd_str]).encode("utf-8")

frame = bytearray([0x81])
length = len(payload)
if length < 126:
    frame.append(0x80 | length)
else:
    frame.append(0x80 | 126)
    frame.extend(length.to_bytes(2, "big"))

mask = os.urandom(4)
frame.extend(mask)
frame.extend(bytes(payload[i] ^ mask[i % 4] for i in range(length)))
sock.sendall(frame)
time.sleep(1.5)
sock.close()
`
	cmd := exec.Command("python3", "-c", pyRunner, wsTermURL, proxyToken, oauthToken)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("terminal exec failed: %v (%s)", err, string(out))
	}
	return nil
}

// FetchDirectColabTelemetry reads `/api/contents/tmp/freegpu_telemetry.json` directly from the Colab VM over Google's API (zero tunnel!).
func FetchDirectColabTelemetry(runtimeName, jupyterURL, proxyToken string) (config.WorkerTelemetry, error) {
	var t config.WorkerTelemetry
	oauthToken, project, err := GetGcloudTokenAndProject()
	if err != nil {
		return t, err
	}

	// If jupyterURL or proxyToken is missing, look up active runtime
	if jupyterURL == "" || proxyToken == "" {
		if runtimeName == "" {
			rtBytes, rtCode, _ := colabRequest("GET", colabAPIBase+"/runtimes", oauthToken, project, nil)
			if rtCode == 200 {
				var rtResp struct {
					Runtimes []ColabRuntime `json:"runtimes"`
				}
				if json.Unmarshal(rtBytes, &rtResp) == nil && len(rtResp.Runtimes) > 0 {
					runtimeName = rtResp.Runtimes[0].Name
				}
			}
		}
		if runtimeName != "" {
			fullBytes, fCode, _ := colabRequest("GET", colabAPIBase+"/"+runtimeName, oauthToken, project, nil)
			if fCode == 200 {
				var fullRT ColabRuntime
				if json.Unmarshal(fullBytes, &fullRT) == nil {
					jupyterURL = fullRT.ConnectionInfo.URL
					proxyToken = fullRT.ConnectionInfo.Token
				}
			}
		}
	}

	if jupyterURL == "" {
		return t, fmt.Errorf("no active Colab runtime")
	}

	baseURL := strings.TrimRight(jupyterURL, "/")
	req, err := http.NewRequest("GET", baseURL+"/api/contents/tmp/freegpu_telemetry.json", nil)
	if err != nil {
		return t, err
	}
	req.Header.Set("X-Colab-Runtime-Proxy-Token", proxyToken)
	req.Header.Set("Authorization", "Bearer "+oauthToken)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return t, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		// Refresh proxyToken from colaboratory.googleapis.com
		if runtimeName != "" {
			fullBytes, fCode, _ := colabRequest("GET", colabAPIBase+"/"+runtimeName, oauthToken, project, nil)
			if fCode == 200 {
				var fullRT ColabRuntime
				if json.Unmarshal(fullBytes, &fullRT) == nil && fullRT.ConnectionInfo.Token != "" {
					proxyToken = fullRT.ConnectionInfo.Token
					jupyterURL = fullRT.ConnectionInfo.URL
				}
			}
		}
	}

	if resp.StatusCode != 200 {
		return t, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var fileObj struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fileObj); err != nil {
		return t, err
	}
	if err := json.Unmarshal([]byte(fileObj.Content), &t); err != nil {
		return t, err
	}

	t.RuntimeName = runtimeName
	t.JupyterURL = jupyterURL
	t.ProxyToken = proxyToken

	// Also touch /api/kernels every poll to keep Colab's idle timer reset!
	kReq, _ := http.NewRequest("GET", baseURL+"/api/kernels", nil)
	kReq.Header.Set("X-Colab-Runtime-Proxy-Token", proxyToken)
	kReq.Header.Set("Authorization", "Bearer "+oauthToken)
	if kResp, err := client.Do(kReq); err == nil {
		_ = kResp.Body.Close()
	}

	return t, nil
}

// StreamOpenAIOverColabKernel forwards an OpenAI /v1/* request directly to `127.0.0.1:8081` inside the Colab VM
// using the `/api/contents` REST bridge (zero WebSockets, zero kernels, zero tunnels!) and streams chunks back.
func StreamOpenAIOverColabKernel(ctx context.Context, jupyterURL, proxyToken, method, path string, reqBody []byte, onChunk func(chunk []byte)) (int, error) {
	oauthToken, _, err := GetGcloudTokenAndProject()
	if err != nil {
		return 502, err
	}
	baseURL := strings.TrimRight(jupyterURL, "/")
	client := colabHTTPClient

	reqID := fmt.Sprintf("r%d", time.Now().UnixNano())
	reqEnvelope, _ := json.Marshal(map[string]string{
		"id":     reqID,
		"method": method,
		"path":   path,
		"body":   string(reqBody),
	})

	putPayload, _ := json.Marshal(map[string]any{
		"type":    "file",
		"format":  "text",
		"content": string(reqEnvelope),
	})

	reqFileURL := fmt.Sprintf("%s/api/contents/tmp/freegpu_req_%s.json", baseURL, reqID)

	// Retry PUT up to 3 times on transient Colab proxy hiccups
	var putErr error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return 499, ctx.Err()
		}
		putReq, err := http.NewRequestWithContext(ctx, "PUT", reqFileURL, bytes.NewReader(putPayload))
		if err != nil {
			return 500, err
		}
		putReq.Header.Set("X-Colab-Runtime-Proxy-Token", proxyToken)
		putReq.Header.Set("Authorization", "Bearer "+oauthToken)
		putReq.Header.Set("Content-Type", "application/json")

		putResp, err := client.Do(putReq)
		if err != nil {
			putErr = err
			time.Sleep(time.Duration(attempt+1) * 400 * time.Millisecond)
			continue
		}
		_ = putResp.Body.Close()
		if putResp.StatusCode >= 400 {
			putErr = fmt.Errorf("Colab PUT HTTP %d", putResp.StatusCode)
			if putResp.StatusCode >= 500 {
				time.Sleep(time.Duration(attempt+1) * 400 * time.Millisecond)
				continue
			}
			return 502, putErr
		}
		putErr = nil
		break
	}
	if putErr != nil {
		return 502, fmt.Errorf("submit request to Colab: %w", putErr)
	}

	respURL := fmt.Sprintf("%s/api/contents/tmp/freegpu_resp_%s.txt", baseURL, reqID)
	cleanupResp := func() {
		delReq, _ := http.NewRequest("DELETE", respURL, nil)
		delReq.Header.Set("X-Colab-Runtime-Proxy-Token", proxyToken)
		delReq.Header.Set("Authorization", "Bearer "+oauthToken)
		if delResp, err := client.Do(delReq); err == nil {
			_ = delResp.Body.Close()
		}
	}

	sentOffset := 0
	deadline := time.Now().Add(8 * time.Minute)
	pollInterval := 120 * time.Millisecond

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			go cleanupResp()
			return 499, ctx.Err()
		case <-time.After(pollInterval):
		}

		getReq, _ := http.NewRequestWithContext(ctx, "GET", respURL, nil)
		getReq.Header.Set("X-Colab-Runtime-Proxy-Token", proxyToken)
		getReq.Header.Set("Authorization", "Bearer "+oauthToken)

		resp, err := client.Do(getReq)
		if err != nil {
			if ctx.Err() != nil {
				go cleanupResp()
				return 499, ctx.Err()
			}
			continue
		}
		if resp.StatusCode != 200 {
			_ = resp.Body.Close()
			continue
		}

		var fileObj struct {
			Content string `json:"content"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&fileObj)
		_ = resp.Body.Close()
		if decodeErr != nil || fileObj.Content == "" {
			continue
		}

		content := fileObj.Content

		// Adaptive poll backoff as file grows so Colab's Tornado server is never saturated
		switch {
		case len(content) > 256*1024:
			pollInterval = 600 * time.Millisecond
		case len(content) > 128*1024:
			pollInterval = 400 * time.Millisecond
		case len(content) > 32*1024:
			pollInterval = 250 * time.Millisecond
		default:
			pollInterval = 120 * time.Millisecond
		}

		eofIdx := strings.Index(content, "__FREEGPU_EOF__")
		if eofIdx != -1 {
			clean := strings.TrimSuffix(content[:eofIdx], "\n")
			go cleanupResp()

			// Propagate upstream llama-server HTTP errors cleanly instead of sending an empty 200 SSE stream
			if sentOffset == 0 && strings.HasPrefix(clean, "__FREEGPU_ERR__:") {
				rest := strings.TrimPrefix(clean, "__FREEGPU_ERR__:")
				parts := strings.SplitN(rest, ":", 2)
				errMsg := rest
				if len(parts) == 2 {
					errMsg = fmt.Sprintf("llama-server HTTP %s: %s", parts[0], strings.TrimSpace(parts[1]))
				}
				return 502, fmt.Errorf("%s", errMsg)
			}

			if len(clean) > sentOffset {
				onChunk([]byte(clean[sentOffset:]))
			}
			return 200, nil
		}

		// Wait if error marker is still being written
		if sentOffset == 0 && strings.HasPrefix(content, "__FREEGPU_ERR__") {
			continue
		}

		// Only emit complete newline-terminated SSE lines during streaming so JSON events are never split mid-line
		if len(content) > sentOffset {
			delta := content[sentOffset:]
			if lastNL := strings.LastIndexByte(delta, '\n'); lastNL != -1 {
				emit := delta[:lastNL+1]
				onChunk([]byte(emit))
				sentOffset += len(emit)
			}
		}
	}

	go cleanupResp()
	return 504, fmt.Errorf("timed out waiting for llama-server response")
}
