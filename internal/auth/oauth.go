package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/anilgoudgunda/freegpu/internal/config"
)

// Build-time injectable Desktop OAuth2 client credentials (set via -ldflags for official .dmg releases).
// Users can also provide FREEGPU_OAUTH_CLIENT_ID / FREEGPU_OAUTH_CLIENT_SECRET or ~/.freegpu/oauth_client.json.
var (
	DefaultClientID     = ""
	DefaultClientSecret = ""
)

const (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
)

var colabScopes = []string{
	"openid",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/colaboratory",
	"https://www.googleapis.com/auth/cloud-platform",
}

type OAuthClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type StoredToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
	Email        string    `json:"email,omitempty"`
	ProjectID    string    `json:"project_id,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
}

type AuthStatus struct {
	SignedIn       bool   `json:"signed_in"`
	Email          string `json:"email,omitempty"`
	Source         string `json:"source,omitempty"` // "oauth", "adc", "gcloud", ""
	HasNativeOAuth bool   `json:"has_native_oauth"`
	ProjectID      string `json:"project_id,omitempty"`
}

type pendingPKCE struct {
	Verifier    string
	RedirectURI string
	CreatedAt   time.Time
}

var (
	mu           sync.Mutex
	cachedToken  string
	cachedProj   string
	cachedSource string
	cachedEmail  string
	cachedExpiry time.Time
	pendingFlows = make(map[string]pendingPKCE)
	httpClient   = &http.Client{Timeout: 15 * time.Second}
)

func tokenFilePath() string {
	return filepath.Join(config.ConfigDir(), "oauth_token.json")
}

func clientFilePath() string {
	return filepath.Join(config.ConfigDir(), "oauth_client.json")
}

// ResolveClient loads OAuth Desktop client credentials from env, ~/.freegpu/oauth_client.json, or build ldflags.
func ResolveClient() (OAuthClient, bool) {
	if id := strings.TrimSpace(os.Getenv("FREEGPU_OAUTH_CLIENT_ID")); id != "" {
		return OAuthClient{
			ClientID:     id,
			ClientSecret: strings.TrimSpace(os.Getenv("FREEGPU_OAUTH_CLIENT_SECRET")),
		}, true
	}

	if data, err := os.ReadFile(clientFilePath()); err == nil {
		// Support Google Cloud Console's {"installed": {...}} format
		var wrapped struct {
			Installed OAuthClient `json:"installed"`
			Web       OAuthClient `json:"web"`
		}
		if json.Unmarshal(data, &wrapped) == nil {
			if wrapped.Installed.ClientID != "" {
				return wrapped.Installed, true
			}
			if wrapped.Web.ClientID != "" {
				return wrapped.Web, true
			}
		}
		// Support flat {"client_id": "...", "client_secret": "..."}
		var flat OAuthClient
		if json.Unmarshal(data, &flat) == nil && flat.ClientID != "" {
			return flat, true
		}
	}

	if strings.TrimSpace(DefaultClientID) != "" {
		return OAuthClient{
			ClientID:     strings.TrimSpace(DefaultClientID),
			ClientSecret: strings.TrimSpace(DefaultClientSecret),
		}, true
	}

	return OAuthClient{}, false
}

func HasNativeClient() bool {
	_, ok := ResolveClient()
	return ok
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// StartLogin creates a PKCE OAuth2 authorization URL targeting http://127.0.0.1:{port}/auth/callback.
func StartLogin(port int) (string, error) {
	client, ok := ResolveClient()
	if !ok {
		return "", fmt.Errorf("no native OAuth client configured (set FREEGPU_OAUTH_CLIENT_ID or place Google Desktop OAuth JSON at ~/.freegpu/oauth_client.json)")
	}

	state, err := randomURLSafe(24)
	if err != nil {
		return "", err
	}
	verifier, err := randomURLSafe(48)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/auth/callback", port)

	mu.Lock()
	// Prune stale flows older than 15m
	for k, v := range pendingFlows {
		if time.Since(v.CreatedAt) > 15*time.Minute {
			delete(pendingFlows, k)
		}
	}
	pendingFlows[state] = pendingPKCE{
		Verifier:    verifier,
		RedirectURI: redirectURI,
		CreatedAt:   time.Now(),
	}
	mu.Unlock()

	params := url.Values{
		"client_id":             {client.ClientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {strings.Join(colabScopes, " ")},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
	}
	return googleAuthURL + "?" + params.Encode(), nil
}

// HandleCallback completes the PKCE OAuth2 code exchange and persists ~/.freegpu/oauth_token.json.
func HandleCallback(w http.ResponseWriter, r *http.Request) {
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		renderCallbackPage(w, false, "Google sign-in cancelled: "+errMsg)
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		renderCallbackPage(w, false, "Missing OAuth code or state parameter.")
		return
	}

	mu.Lock()
	flow, ok := pendingFlows[state]
	delete(pendingFlows, state)
	mu.Unlock()
	if !ok {
		renderCallbackPage(w, false, "Invalid or expired OAuth state. Please try signing in again from FreeGPU.")
		return
	}

	client, ok := ResolveClient()
	if !ok {
		renderCallbackPage(w, false, "OAuth client configuration missing.")
		return
	}

	form := url.Values{
		"client_id":     {client.ClientID},
		"code":          {code},
		"code_verifier": {flow.Verifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {flow.RedirectURI},
	}
	if client.ClientSecret != "" {
		form.Set("client_secret", client.ClientSecret)
	}

	tok, err := exchangeToken(form)
	if err != nil {
		renderCallbackPage(w, false, "Token exchange failed: "+err.Error())
		return
	}

	tok.ClientID = client.ClientID
	if email := fetchUserEmail(tok.AccessToken); email != "" {
		tok.Email = email
	}

	if err := saveStoredToken(tok); err != nil {
		renderCallbackPage(w, false, "Failed to save token: "+err.Error())
		return
	}

	mu.Lock()
	cachedToken = tok.AccessToken
	cachedProj = tok.ProjectID
	cachedSource = "oauth"
	cachedEmail = tok.Email
	cachedExpiry = tok.Expiry
	mu.Unlock()

	msg := "Signed in to FreeGPU."
	if tok.Email != "" {
		msg = fmt.Sprintf("Signed in as %s.", tok.Email)
	}
	renderCallbackPage(w, true, msg+" You can close this tab and return to FreeGPU.")
}

func renderCallbackPage(w http.ResponseWriter, ok bool, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	accent := "#3ECF8E"
	title := "FREEGPU // AUTHENTICATED"
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		accent = "#EF4444"
		title = "FREEGPU // AUTH ERROR"
	}
	fmt.Fprintf(w, `<!doctype html>
<html><head><meta charset="utf-8"><title>%s</title>
<style>
body{background:#0A0A0A;color:#EDEDED;font-family:"SF Mono",Menlo,monospace;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.box{border:1px solid %s;background:#111311;padding:24px 28px;max-width:460px}
.tag{display:inline-block;background:%s;color:#0A0A0A;font-weight:700;padding:2px 8px;font-size:11px;letter-spacing:.08em;margin-bottom:14px}
p{margin:0;font-size:13px;line-height:1.6;color:#EDEDED}
</style></head>
<body><div class="box"><div class="tag">%s</div><p>%s</p></div></body></html>`,
		title, accent, accent, title, message)
}

func exchangeToken(form url.Values) (*StoredToken, error) {
	resp, err := httpClient.PostForm(googleTokenURL, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if raw.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in response")
	}
	if raw.ExpiresIn <= 0 {
		raw.ExpiresIn = 3600
	}
	return &StoredToken{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second),
	}, nil
}

func fetchUserEmail(accessToken string) string {
	req, err := http.NewRequest("GET", googleUserInfoURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := httpClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()
	var info struct {
		Email string `json:"email"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&info)
	return info.Email
}

const (
	keychainService = "com.anilgoudgunda.freegpu"
	keychainAccount = "oauth_token"
)

func saveStoredToken(tok *StoredToken) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		// Store securely in macOS Login Keychain (base64 encoded to avoid newline/quoting issues)
		encoded := base64.StdEncoding.EncodeToString(b)
		cmd := exec.Command("security", "add-generic-password",
			"-U",
			"-s", keychainService,
			"-a", keychainAccount,
			"-w", encoded,
		)
		if err := cmd.Run(); err == nil {
			// Remove any legacy plaintext token file once safely in Keychain
			_ = os.Remove(tokenFilePath())
			return nil
		}
	}
	if err := os.MkdirAll(config.ConfigDir(), 0700); err != nil {
		return err
	}
	pretty, _ := json.MarshalIndent(tok, "", "  ")
	return os.WriteFile(tokenFilePath(), pretty, 0600)
}

func loadStoredToken() (*StoredToken, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("security", "find-generic-password",
			"-s", keychainService,
			"-a", keychainAccount,
			"-w",
		).Output()
		if err == nil {
			raw := strings.TrimSpace(string(out))
			if decoded, decErr := base64.StdEncoding.DecodeString(raw); decErr == nil {
				var tok StoredToken
				if json.Unmarshal(decoded, &tok) == nil {
					return &tok, nil
				}
			}
		}
	}

	data, err := os.ReadFile(tokenFilePath())
	if err != nil {
		return nil, err
	}
	var tok StoredToken
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, err
	}
	// Transparently migrate legacy plaintext file into macOS Keychain
	if runtime.GOOS == "darwin" {
		_ = saveStoredToken(&tok)
	}
	return &tok, nil
}

// GetAccessToken returns a valid Google OAuth2 access token without requiring the `gcloud` CLI binary.
// Priority:
//  1. In-memory cached token
//  2. Native FreeGPU OAuth token (~/.freegpu/oauth_token.json), auto-refreshed in pure Go
//  3. Existing gcloud ADC JSON (~/.config/gcloud/application_default_credentials.json), auto-refreshed in pure Go
func GetAccessToken() (token, project, source string, err error) {
	mu.Lock()
	if cachedToken != "" && time.Now().Add(2*time.Minute).Before(cachedExpiry) {
		t, p, s := cachedToken, cachedProj, cachedSource
		mu.Unlock()
		return t, p, s, nil
	}
	mu.Unlock()

	// 1. Native FreeGPU OAuth token
	if tok, err := loadStoredToken(); err == nil && (tok.AccessToken != "" || tok.RefreshToken != "") {
		if tok.AccessToken != "" && time.Now().Add(2*time.Minute).Before(tok.Expiry) {
			mu.Lock()
			cachedToken, cachedProj, cachedSource, cachedEmail, cachedExpiry = tok.AccessToken, tok.ProjectID, "oauth", tok.Email, tok.Expiry
			mu.Unlock()
			return tok.AccessToken, tok.ProjectID, "oauth", nil
		}
		if tok.RefreshToken != "" {
			if client, ok := ResolveClient(); ok {
				form := url.Values{
					"client_id":     {client.ClientID},
					"refresh_token": {tok.RefreshToken},
					"grant_type":    {"refresh_token"},
				}
				if client.ClientSecret != "" {
					form.Set("client_secret", client.ClientSecret)
				}
				if refreshed, err := exchangeToken(form); err == nil {
					tok.AccessToken = refreshed.AccessToken
					tok.Expiry = refreshed.Expiry
					if refreshed.RefreshToken != "" {
						tok.RefreshToken = refreshed.RefreshToken
					}
					_ = saveStoredToken(tok)
					mu.Lock()
					cachedToken, cachedProj, cachedSource, cachedEmail, cachedExpiry = tok.AccessToken, tok.ProjectID, "oauth", tok.Email, tok.Expiry
					mu.Unlock()
					return tok.AccessToken, tok.ProjectID, "oauth", nil
				}
			}
		}
	}

	// 2. Pure-Go refresh of ~/.config/gcloud/application_default_credentials.json (no gcloud binary needed)
	if tok, proj, exp, err := refreshADCFile(); err == nil && tok != "" {
		mu.Lock()
		cachedToken, cachedProj, cachedSource, cachedExpiry = tok, proj, "adc", exp
		mu.Unlock()
		return tok, proj, "adc", nil
	}

	return "", "", "", fmt.Errorf("not signed in")
}

func adcFilePath() string {
	if env := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
}

func refreshADCFile() (token, project string, expiry time.Time, err error) {
	p := adcFilePath()
	if p == "" {
		return "", "", time.Time{}, fmt.Errorf("no ADC path")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", "", time.Time{}, err
	}
	var adc struct {
		Type           string `json:"type"`
		ClientID       string `json:"client_id"`
		ClientSecret   string `json:"client_secret"`
		RefreshToken   string `json:"refresh_token"`
		QuotaProjectID string `json:"quota_project_id"`
	}
	if err := json.Unmarshal(data, &adc); err != nil {
		return "", "", time.Time{}, err
	}
	if adc.Type != "authorized_user" || adc.RefreshToken == "" || adc.ClientID == "" {
		return "", "", time.Time{}, fmt.Errorf("ADC file is not an authorized_user credential")
	}

	form := url.Values{
		"client_id":     {adc.ClientID},
		"client_secret": {adc.ClientSecret},
		"refresh_token": {adc.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	refreshed, err := exchangeToken(form)
	if err != nil {
		return "", adc.QuotaProjectID, time.Time{}, err
	}
	return refreshed.AccessToken, adc.QuotaProjectID, refreshed.Expiry, nil
}

// Logout clears the stored FreeGPU OAuth token (Keychain + file) and memory cache.
func Logout() error {
	mu.Lock()
	cachedToken = ""
	cachedProj = ""
	cachedSource = ""
	cachedEmail = ""
	cachedExpiry = time.Time{}
	mu.Unlock()
	if runtime.GOOS == "darwin" {
		_ = exec.Command("security", "delete-generic-password",
			"-s", keychainService,
			"-a", keychainAccount,
		).Run()
	}
	if err := os.Remove(tokenFilePath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Status returns the current authentication state for the UI.
func Status() AuthStatus {
	st := AuthStatus{
		HasNativeOAuth: HasNativeClient(),
	}
	tok, proj, src, err := GetAccessToken()
	if err == nil && tok != "" {
		st.SignedIn = true
		st.Source = src
		st.ProjectID = proj
		mu.Lock()
		st.Email = cachedEmail
		mu.Unlock()
	}
	return st
}
