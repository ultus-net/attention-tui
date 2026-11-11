package graph

import (
	"context"
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
