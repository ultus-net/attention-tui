// Package auth runs the authorization-code + PKCE flow and keeps one
// delegated token alive. Tokens are never logged.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/ultus-net/attention-tui/internal/config"
)

const (
	authorizeTemplate = "https://login.microsoftonline.com/%s/oauth2/v2.0/authorize"
	tokenTemplate     = "https://login.microsoftonline.com/%s/oauth2/v2.0/token"
	keyringService    = "attention-tui"
	redirectPath      = "/callback"
	refreshSlack      = 60 * time.Second
	callbackTimeout   = 5 * time.Minute
)

// ErrNoToken means there is no usable delegated token. The UI prints a banner
// and keeps running; the operator runs --login.
var ErrNoToken = errors.New("auth: no token; run attention-tui --login")

// Tokens is the persisted credential. The refresh token is the durable part.
type Tokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

// Store persists tokens. FileStore is the default; KeyringStore is available
// for operators who keep secrets in the OS keyring.
type Store interface {
	Load() (Tokens, bool, error)
	Save(Tokens) error
}

// NewFileStore keeps tokens in path with mode 0600.
func NewFileStore(path string) Store {
	return &fileStore{path: path}
}

type fileStore struct {
	path string
}

func (f *fileStore) Load() (Tokens, bool, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return Tokens{}, false, nil
	}
	if err != nil {
		return Tokens{}, false, fmt.Errorf("auth: read token file: %w", err)
	}
	var t Tokens
	if err := json.Unmarshal(data, &t); err != nil {
		return Tokens{}, false, fmt.Errorf("auth: parse token file: %w", err)
	}
	return t, true, nil
}

func (f *fileStore) Save(t Tokens) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("auth: encode tokens: %w", err)
	}
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("auth: create token dir: %w", err)
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("auth: write token file: %w", err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("auth: replace token file: %w", err)
	}
	return nil
}

// NewKeyringStore keeps tokens in the OS keyring under service/user.
func NewKeyringStore(service, user string) Store {
	return &keyringStore{service: service, user: user}
}

type keyringStore struct {
	service string
	user    string
}

func (k *keyringStore) Load() (Tokens, bool, error) {
	raw, err := keyring.Get(k.service, k.user)
	if errors.Is(err, keyring.ErrNotFound) {
		return Tokens{}, false, nil
	}
	if err != nil {
		return Tokens{}, false, fmt.Errorf("auth: read keyring: %w", err)
	}
	var t Tokens
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return Tokens{}, false, fmt.Errorf("auth: parse keyring token: %w", err)
	}
	return t, true, nil
}

func (k *keyringStore) Save(t Tokens) error {
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("auth: encode tokens: %w", err)
	}
	if err := keyring.Set(k.service, k.user, string(data)); err != nil {
		return fmt.Errorf("auth: write keyring: %w", err)
	}
	return nil
}

// Manager owns one token. Token and Refresh serialize on a mutex so concurrent
// callers produce exactly one token-endpoint request.
type Manager struct {
	cfg          config.Auth
	scopes       []string
	store        Store
	http         *http.Client
	authorizeURL string
	tokenURL     string
	now          func() time.Time

	mu sync.Mutex
}

// NewManager builds a manager for one app registration.
func NewManager(cfg config.Auth, scopes []string, st Store) *Manager {
	tenant := strings.TrimSpace(cfg.Tenant)
	if tenant == "" {
		tenant = "organizations"
	}
	return &Manager{
		cfg:          cfg,
		scopes:       scopes,
		store:        st,
		http:         &http.Client{Timeout: 30 * time.Second},
		authorizeURL: fmt.Sprintf(authorizeTemplate, url.PathEscape(tenant)),
		tokenURL:     fmt.Sprintf(tokenTemplate, url.PathEscape(tenant)),
		now:          time.Now,
	}
}

// Token returns a valid access token, refreshing under the mutex when the
// cached one is expired or about to expire.
func (m *Manager) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tokens, ok, err := m.store.Load()
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(tokens.RefreshToken) == "" {
		return "", ErrNoToken
	}
	if tokens.AccessToken != "" && m.now().Before(tokens.Expiry.Add(-refreshSlack)) {
		return tokens.AccessToken, nil
	}
	return m.refreshLocked(ctx, tokens)
}

// Refresh forces a refresh. The graph client calls it after a 401.
func (m *Manager) Refresh(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tokens, ok, err := m.store.Load()
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(tokens.RefreshToken) == "" {
		return "", ErrNoToken
	}
	return m.refreshLocked(ctx, tokens)
}

func (m *Manager) refreshLocked(ctx context.Context, tokens Tokens) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", tokens.RefreshToken)
	form.Set("client_id", m.cfg.ClientID)
	form.Set("scope", strings.Join(m.scopes, " "))
	if m.cfg.Mode == "confidential" {
		secret, err := keyring.Get(keyringService, m.cfg.ClientID)
		if err != nil {
			return "", fmt.Errorf("auth: read client secret: %w", err)
		}
		form.Set("client_secret", secret)
	}

	payload, err := m.postToken(ctx, form)
	if err != nil {
		return "", err
	}

	refreshed := Tokens{
		AccessToken:  payload.AccessToken,
		RefreshToken: tokens.RefreshToken,
		Expiry:       m.now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}
	if payload.RefreshToken != "" {
		refreshed.RefreshToken = payload.RefreshToken
	}
	// Persist before the access token is used, so a rotated refresh token
	// cannot be lost to a crash after the next call succeeds.
	if err := m.store.Save(refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

type tokenPayload struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (m *Manager) postToken(ctx context.Context, form url.Values) (tokenPayload, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenPayload{}, fmt.Errorf("auth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.http.Do(req)
	if err != nil {
		return tokenPayload{}, fmt.Errorf("auth: token request: %w", err)
	}
	defer resp.Body.Close()

	var payload tokenPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return tokenPayload{}, fmt.Errorf("auth: decode token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || payload.AccessToken == "" {
		if payload.Error == "invalid_grant" {
			return tokenPayload{}, ErrNoToken
		}
		detail := strings.TrimSpace(payload.ErrorDescription)
		if detail == "" {
			detail = strings.TrimSpace(payload.Error)
		}
		if detail == "" {
			detail = resp.Status
		}
		return tokenPayload{}, fmt.Errorf("auth: token endpoint: %s", detail)
	}
	if payload.ExpiresIn <= 0 {
		payload.ExpiresIn = 3600
	}
	return payload, nil
}

// Login runs the authorization-code + PKCE flow: open the browser, receive the
// code on 127.0.0.1, exchange it, and persist the tokens.
func (m *Manager) Login(ctx context.Context, open func(string) error) error {
	verifier, err := randomURLSafe(32)
	if err != nil {
		return err
	}
	state, err := randomURLSafe(16)
	if err != nil {
		return err
	}
	redirectURI := fmt.Sprintf("http://localhost:%d%s", m.cfg.RedirectPort, redirectPath)

	authURL, err := url.Parse(m.authorizeURL)
	if err != nil {
		return fmt.Errorf("auth: build authorize URL: %w", err)
	}
	query := authURL.Query()
	query.Set("client_id", m.cfg.ClientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", redirectURI)
	query.Set("response_mode", "query")
	query.Set("scope", strings.Join(m.scopes, " "))
	query.Set("state", state)
	query.Set("code_challenge", PKCEChallenge(verifier))
	query.Set("code_challenge_method", "S256")
	authURL.RawQuery = query.Encode()

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(redirectPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if msg := q.Get("error"); msg != "" {
			errCh <- fmt.Errorf("auth: authorize failed: %s %s", msg, q.Get("error_description"))
			http.Error(w, "authorization failed; return to the terminal", http.StatusBadRequest)
			return
		}
		if q.Get("state") != state {
			errCh <- errors.New("auth: state mismatch; refusing the callback")
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		if code == "" {
			errCh <- errors.New("auth: callback carried no code")
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		codeCh <- code
		fmt.Fprintln(w, "Signed in. You can close this window.")
	})

	addr := fmt.Sprintf("127.0.0.1:%d", m.cfg.RedirectPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("auth: listen on %s: %w", addr, err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(listener)
	defer server.Shutdown(context.Background())

	if err := open(authURL.String()); err != nil {
		return fmt.Errorf("auth: open browser: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, callbackTimeout)
	defer cancel()

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return err
	case <-waitCtx.Done():
		return fmt.Errorf("auth: no callback on %s: %w", addr, waitCtx.Err())
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", m.cfg.ClientID)
	form.Set("scope", strings.Join(m.scopes, " "))
	form.Set("code_verifier", verifier)
	if m.cfg.Mode == "confidential" {
		secret, err := keyring.Get(keyringService, m.cfg.ClientID)
		if err != nil {
			return fmt.Errorf("auth: read client secret: %w", err)
		}
		form.Set("client_secret", secret)
	}

	payload, err := m.postToken(ctx, form)
	if err != nil {
		return err
	}
	tokens := Tokens{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		Expiry:       m.now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}
	if strings.TrimSpace(tokens.RefreshToken) == "" {
		return errors.New("auth: no refresh token returned; the app needs offline_access")
	}
	return m.store.Save(tokens)
}

// PKCEChallenge is base64url(sha256(verifier)) without padding.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomURLSafe(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generate random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
