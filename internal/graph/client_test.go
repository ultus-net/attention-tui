package graph

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeTokens struct {
	calls atomic.Int32
}

func (f *fakeTokens) Token(ctx context.Context) (string, error) {
	return fmt.Sprintf("token-%d", f.calls.Add(1)), nil
}

func TestRetriesOn429WithRetryAfter(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"value":"ok"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, &fakeTokens{})
	var out struct {
		Value string `json:"value"`
	}
	if err := c.get(context.Background(), "me/test", &out); err != nil {
		t.Fatalf("get: %v", err)
	}
	if out.Value != "ok" {
		t.Fatalf("value = %q, want ok", out.Value)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

func TestRetriesOn503(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, &fakeTokens{})
	if err := c.get(context.Background(), "me/test", nil); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

func TestUnauthorizedRefreshesOnce(t *testing.T) {
	var requests atomic.Int32
	var secondAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		secondAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tokens := &fakeTokens{}
	c := New(srv.URL, tokens)
	if err := c.get(context.Background(), "me/test", nil); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := tokens.calls.Load(); got != 2 {
		t.Fatalf("token calls = %d, want 2", got)
	}
	if secondAuth != "Bearer token-2" {
		t.Fatalf("second Authorization = %q, want the refreshed token", secondAuth)
	}
}

func TestUnauthorizedTwiceReturnsErrUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, &fakeTokens{})
	err := c.get(context.Background(), "me/test", nil)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestGoneMapsToErrGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	c := New(srv.URL, &fakeTokens{})
	err := c.get(context.Background(), "me/test", nil)
	if !errors.Is(err, ErrGone) {
		t.Fatalf("err = %v, want ErrGone", err)
	}
}

func TestErrorBodyIsSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":"AccessDenied","message":"Insufficient privileges"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, &fakeTokens{})
	err := c.get(context.Background(), "me/test", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("err = %v, want the Graph error code", err)
	}
}

func TestContextCancelledStopsRetry(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := New(srv.URL, &fakeTokens{})
	err := c.get(ctx, "me/test", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
			w.Header().Set("Retry-After", past)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, &fakeTokens{})
	if err := c.get(context.Background(), "me/test", nil); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

func TestResolveRejectsForeignOrigin(t *testing.T) {
	c := New("https://graph.microsoft.com/v1.0", &fakeTokens{})
	for _, raw := range []string{
		"https://evil.example/v1.0/me/messages",
		"http://graph.microsoft.com/v1.0/me/messages",
		"https://graph.microsoft.com:8443/v1.0/me/messages",
		"//evil.example/v1.0/me/messages",
		"https://user@graph.microsoft.com/v1.0/me",
	} {
		if _, err := c.resolve(raw); !errors.Is(err, ErrUntrustedHost) {
			t.Errorf("resolve(%q) error = %v, want ErrUntrustedHost", raw, err)
		}
	}
}

func TestResolveKeepsBaseOriginURLByteForByte(t *testing.T) {
	c := New("https://graph.microsoft.com/v1.0", &fakeTokens{})
	raw := "https://graph.microsoft.com/v1.0/me/chats?$skiptoken=a%2Fb%24c"
	got, err := c.resolve(raw)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != raw {
		t.Fatalf("resolve = %q, want the link verbatim", got)
	}
}

func TestResolveJoinsRelativePath(t *testing.T) {
	c := New("https://graph.microsoft.com/v1.0", &fakeTokens{})
	got, err := c.resolve("/me/chats?$top=50")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "https://graph.microsoft.com/v1.0/me/chats?$top=50" {
		t.Fatalf("resolve = %q", got)
	}
}

func TestGetDoesNotSendTokenToForeignHost(t *testing.T) {
	var hits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{}`))
	}))
	defer evil.Close()

	home := New("https://graph.microsoft.com/v1.0", &fakeTokens{})
	err := home.get(context.Background(), evil.URL+"/v1.0/me/messages", nil)
	if !errors.Is(err, ErrUntrustedHost) {
		t.Fatalf("err = %v, want ErrUntrustedHost", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("foreign host received %d requests, want 0", got)
	}
}

func TestRedirectToForeignHostIsRefused(t *testing.T) {
	var hits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{}`))
	}))
	defer evil.Close()

	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/v1.0/me/messages", http.StatusTemporaryRedirect)
	}))
	defer home.Close()

	c := New(home.URL, &fakeTokens{})
	err := c.get(context.Background(), "me/messages", nil)
	if !errors.Is(err, ErrUntrustedHost) {
		t.Fatalf("err = %v, want ErrUntrustedHost", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("foreign host received %d requests, want 0", got)
	}
}

func TestSameOriginRedirectIsFollowed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/me/test" {
			http.Redirect(w, r, "/me/other", http.StatusFound)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"value":"ok"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, &fakeTokens{})
	var out struct {
		Value string `json:"value"`
	}
	if err := c.get(context.Background(), "me/test", &out); err != nil {
		t.Fatalf("get: %v", err)
	}
	if out.Value != "ok" {
		t.Fatalf("value = %q, want ok", out.Value)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("followed request count = %d, want 1", got)
	}
}

type claimsTokens struct {
	fakeTokens
	gotClaims string
}

func (c *claimsTokens) Refresh(context.Context) (string, error) { return "plain-token", nil }

func (c *claimsTokens) RefreshWithClaims(_ context.Context, claims string) (string, error) {
	c.gotClaims = claims
	return "claimed-token", nil
}

func TestClaimsChallengeDecodesBase64URL(t *testing.T) {
	claims := `{"access_token":{"nbf":{"essential":true,"value":"1"}}}`
	encoded := base64.RawURLEncoding.EncodeToString([]byte(claims))
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("WWW-Authenticate", `Bearer realm="", error="insufficient_claims", claims="`+encoded+`"`)

	if got := claimsChallenge(resp); got != claims {
		t.Fatalf("claims = %q, want the decoded JSON", got)
	}
}

func TestClaimsChallengeIgnoresMalformedValue(t *testing.T) {
	for _, header := range []string{
		`Bearer error="insufficient_claims", claims="not base64!"`,
		`Bearer error="insufficient_claims", claims="` + base64.RawURLEncoding.EncodeToString([]byte("not json")) + `"`,
		`Bearer error="invalid_token", claims="` + base64.RawURLEncoding.EncodeToString([]byte(`{"a":1}`)) + `"`,
	} {
		resp := &http.Response{Header: http.Header{}}
		resp.Header.Set("WWW-Authenticate", header)
		if got := claimsChallenge(resp); got != "" {
			t.Errorf("header %q produced claims %q, want none", header, got)
		}
	}
}

func TestClaimsChallengeRefreshesWithClaims(t *testing.T) {
	claims := `{"access_token":{"nbf":{"essential":true,"value":"1"}}}`
	encoded := base64.RawURLEncoding.EncodeToString([]byte(claims))
	var requests int
	var secondAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="", error="insufficient_claims", claims="`+encoded+`"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		secondAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"value":"ok"}`))
	}))
	defer srv.Close()

	tokens := &claimsTokens{}
	c := New(srv.URL, tokens)
	var out struct {
		Value string `json:"value"`
	}
	if err := c.get(context.Background(), "me/test", &out); err != nil {
		t.Fatalf("get: %v", err)
	}
	if tokens.gotClaims != claims {
		t.Fatalf("claims = %q, want the decoded challenge", tokens.gotClaims)
	}
	if secondAuth != "Bearer claimed-token" {
		t.Fatalf("Authorization = %q, want the claims-refreshed token", secondAuth)
	}
	if out.Value != "ok" {
		t.Fatalf("value = %q", out.Value)
	}
}

func TestRetryBudgetExhausted(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"code":"InternalServerError","message":"boom"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, &fakeTokens{})
	err := c.get(context.Background(), "me/test", nil)
	if err == nil {
		t.Fatal("expected the exhausted retry budget to return an error")
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want the initial call plus two retries", got)
	}
}
