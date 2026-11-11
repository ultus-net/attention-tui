package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ultus-net/attention-tui/internal/config"
)

func TestPKCEChallengeIsS256(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if got := PKCEChallenge(verifier); got != want {
		t.Fatalf("PKCEChallenge = %q, want %q", got, want)
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	st := NewFileStore(path)
	if _, ok, err := st.Load(); err != nil || ok {
		t.Fatalf("Load on a missing file = _, %v, %v", ok, err)
	}
	want := Tokens{
		AccessToken:  "access",
		RefreshToken: "refresh",
		Expiry:       time.Unix(1_800_000_000, 0).UTC(),
	}
	if err := st.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := st.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok || got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken || !got.Expiry.Equal(want.Expiry) {
		t.Fatalf("Load = %+v, %v", got, ok)
	}
}

func TestFileStorePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	st := NewFileStore(path)
	if err := st.Save(Tokens{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
}

type memStore struct {
	mu       sync.Mutex
	tokens   Tokens
	ok       bool
	saves    int
	lastSave Tokens
}

func (m *memStore) Load() (Tokens, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokens, m.ok, nil
}

func (m *memStore) Save(t Tokens) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens = t
	m.ok = true
	m.saves++
	m.lastSave = t
	return nil
}

func testManager(st Store, tokenURL string) *Manager {
	m := NewManager(config.Auth{ClientID: "client-id", Tenant: "organizations", Mode: "public"}, []string{"User.Read"}, st)
	m.tokenURL = tokenURL
	return m
}

func tokenServer(t *testing.T, hits *atomic.Int32, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if r.PostForm.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q", r.PostForm.Get("grant_type"))
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExpiredTokenRefreshes(t *testing.T) {
	var hits atomic.Int32
	srv := tokenServer(t, &hits, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`, http.StatusOK)
	st := &memStore{tokens: Tokens{AccessToken: "old", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Minute)}, ok: true}
	m := testManager(st, srv.URL)

	token, err := m.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if token != "new-access" {
		t.Fatalf("token = %q", token)
	}
	if hits.Load() != 1 {
		t.Fatalf("token endpoint hits = %d, want 1", hits.Load())
	}
}

func TestRefreshRotationPersistedFirst(t *testing.T) {
	var hits atomic.Int32
	srv := tokenServer(t, &hits, `{"access_token":"new-access","refresh_token":"rotated","expires_in":3600}`, http.StatusOK)
	st := &memStore{tokens: Tokens{AccessToken: "old", RefreshToken: "original", Expiry: time.Now().Add(-time.Minute)}, ok: true}
	m := testManager(st, srv.URL)

	if _, err := m.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.lastSave.RefreshToken != "rotated" {
		t.Fatalf("persisted refresh token = %q, want rotated", st.lastSave.RefreshToken)
	}
	if st.lastSave.AccessToken != "new-access" {
		t.Fatalf("persisted access token = %q", st.lastSave.AccessToken)
	}
}

func TestRefreshIsSingleFlight(t *testing.T) {
	var hits atomic.Int32
	srv := tokenServer(t, &hits, `{"access_token":"new-access","refresh_token":"rotated","expires_in":3600}`, http.StatusOK)
	st := &memStore{tokens: Tokens{AccessToken: "old", RefreshToken: "original", Expiry: time.Now().Add(-time.Minute)}, ok: true}
	m := testManager(st, srv.URL)

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := m.Token(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if token != "new-access" {
				errs <- errors.New("unexpected token " + token)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Token: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("token endpoint hits = %d, want 1", hits.Load())
	}
}

func TestRevokedRefreshReturnsErrNoToken(t *testing.T) {
	var hits atomic.Int32
	srv := tokenServer(t, &hits, `{"error":"invalid_grant","error_description":"revoked"}`, http.StatusBadRequest)
	st := &memStore{tokens: Tokens{AccessToken: "old", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Minute)}, ok: true}
	m := testManager(st, srv.URL)

	_, err := m.Token(context.Background())
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestMissingTokenReturnsErrNoToken(t *testing.T) {
	m := testManager(&memStore{}, "http://127.0.0.1:1/token")
	_, err := m.Token(context.Background())
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}
