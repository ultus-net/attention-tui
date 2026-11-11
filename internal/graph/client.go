// Package graph is a small typed REST client for the Microsoft Graph
// endpoints attention-tui needs. No SDK, no webhooks.
package graph

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the v1.0 Graph root.
const DefaultBaseURL = "https://graph.microsoft.com/v1.0"

const (
	maxRetries    = 2
	maxRetryAfter = 300 * time.Second
	maxErrorBody  = 64 << 10
)

var (
	// ErrUnauthorized means the token was rejected twice in a row.
	ErrUnauthorized = errors.New("graph: unauthorized")
	// ErrGone means a delta link expired and the caller must restart the walk.
	ErrGone = errors.New("graph: gone")
	// ErrUntrustedHost means a request URL would send the bearer token to a
	// host or scheme other than the configured Graph origin.
	ErrUntrustedHost = errors.New("graph: untrusted request host")
)

// TokenSource supplies a bearer token. Implementations should return the
// cached token and refresh it only when it is close to expiry.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// tokenRefresher is implemented by sources that can force a refresh after a
// 401. Client uses it when available.
type tokenRefresher interface {
	Refresh(ctx context.Context) (string, error)
}

// claimsRefresher is implemented by sources that can answer a Conditional
// Access claims challenge carried by a 401. Client prefers it over
// tokenRefresher when the response names claims.
type claimsRefresher interface {
	RefreshWithClaims(ctx context.Context, claims string) (string, error)
}

// Client is safe for concurrent use by multiple goroutines.
type Client struct {
	baseURL string
	tokens  TokenSource
	http    *http.Client

	selfMu     sync.Mutex
	selfUserID string
}

// New builds a client. An empty baseURL means the public Graph endpoint.
func New(baseURL string, ts TokenSource) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		tokens:  ts,
		http: &http.Client{
			Timeout:       60 * time.Second,
			CheckRedirect: checkRedirect,
		},
	}
}

// checkRedirect refuses to carry a request, and its bearer token, to another
// origin. Graph answers with a same-origin redirect or none at all.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("graph: stopped after 10 redirects")
	}
	if len(via) == 0 {
		return nil
	}
	// Every hop must stay on the original origin. Checking the immediate
	// predecessor as well keeps the rule independent of how the chain was built.
	if !sameOrigin(via[0].URL, req.URL) || !sameOrigin(via[len(via)-1].URL, req.URL) {
		return ErrUntrustedHost
	}
	return nil
}

// get performs a GET and decodes the JSON body into out. It retries 429 and
// 5xx twice, honours Retry-After, and refreshes the token once on a 401.
func (c *Client) get(ctx context.Context, url string, out any) error {
	return c.getWithPrefer(ctx, url, "", out)
}

// getWithPrefer is get plus one Prefer header value. Graph needs it on
// endpoints that ignore $top, such as the mail delta.
func (c *Client) getWithPrefer(ctx context.Context, url, prefer string, out any) error {
	endpoint, err := c.resolve(url)
	if err != nil {
		return err
	}
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("graph: acquire token: %w", err)
	}

	retries := 0
	refreshed := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := c.do(ctx, endpoint, prefer, token)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("graph: GET %s: %w", url, err)
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			err := decode(resp.Body, out)
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("graph: decode %s: %w", url, err)
			}
			return nil
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		claims := claimsChallenge(resp)
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("graph: read error body: %w", readErr)
		}

		switch {
		case resp.StatusCode == http.StatusUnauthorized && !refreshed:
			refreshed = true
			token, err = c.forceTokenWithClaims(ctx, claims)
			if err != nil {
				return fmt.Errorf("graph: refresh after 401: %w", err)
			}
			continue
		case retryable(resp.StatusCode) && retries < maxRetries:
			retries++
			if err := sleep(ctx, retryAfter(resp, retries)); err != nil {
				return err
			}
			continue
		default:
			return newGraphError(resp.StatusCode, body)
		}
	}
}

func (c *Client) do(ctx context.Context, endpoint, prefer, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if prefer != "" {
		req.Header.Set("Prefer", prefer)
	}
	return c.http.Do(req)
}

func (c *Client) forceToken(ctx context.Context) (string, error) {
	if r, ok := c.tokens.(tokenRefresher); ok {
		return r.Refresh(ctx)
	}
	return c.tokens.Token(ctx)
}

// forceTokenWithClaims honors a Conditional Access claims challenge. When the
// source cannot answer claims, it falls back to a plain refresh so a server
// that sends a stray challenge cannot wedge the client.
func (c *Client) forceTokenWithClaims(ctx context.Context, claims string) (string, error) {
	if claims != "" {
		if r, ok := c.tokens.(claimsRefresher); ok {
			return r.RefreshWithClaims(ctx, claims)
		}
	}
	return c.forceToken(ctx)
}

// claimsChallenge returns the decoded claims JSON from a 401 response, or "".
// Graph sends it as a base64url value in the WWW-Authenticate header. A value
// that does not decode to JSON is ignored, because a malformed challenge must
// not change how the token is refreshed.
func claimsChallenge(resp *http.Response) string {
	for _, header := range resp.Header.Values("WWW-Authenticate") {
		params := authParams(header)
		if !strings.EqualFold(params["error"], "insufficient_claims") {
			continue
		}
		raw := params["claims"]
		if raw == "" {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			decoded, err = base64.URLEncoding.DecodeString(raw)
			if err != nil {
				continue
			}
		}
		if json.Valid(decoded) {
			return string(decoded)
		}
	}
	return ""
}

// authParams parses a WWW-Authenticate header into its key/value pairs. It
// understands quoted values, including backslash escapes, and stops at a
// malformed pair rather than guessing.
func authParams(header string) map[string]string {
	out := make(map[string]string)
	if i := strings.IndexByte(header, ' '); i >= 0 {
		header = header[i+1:]
	}
	for {
		header = strings.TrimLeft(header, " ,")
		if header == "" {
			return out
		}
		eq := strings.IndexByte(header, '=')
		if eq < 0 {
			return out
		}
		key := strings.ToLower(strings.TrimSpace(header[:eq]))
		header = header[eq+1:]

		var value string
		if strings.HasPrefix(header, `"`) {
			header = header[1:]
			var b strings.Builder
			closed := false
			escaped := false
			for i := 0; i < len(header); i++ {
				switch {
				case escaped:
					b.WriteByte(header[i])
					escaped = false
				case header[i] == '\\':
					escaped = true
				case header[i] == '"':
					header = header[i+1:]
					closed = true
				default:
					b.WriteByte(header[i])
				}
				if closed {
					break
				}
			}
			if !closed {
				header = ""
			}
			value = b.String()
		} else {
			end := strings.IndexByte(header, ',')
			if end < 0 {
				value = header
				header = ""
			} else {
				value = header[:end]
				header = header[end:]
			}
		}
		out[key] = value
	}
}

// resolve turns a Graph path or continuation link into an absolute URL. An
// absolute URL must stay on the configured origin, because the bearer token
// goes with it. The origin is scheme, host, and port; the base path is not
// part of the trust decision, because Graph continuation links may point at
// any path on the same service. A nextLink from a compromised or spoofed
// response cannot move the token to another host.
func (c *Client) resolve(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("graph: invalid request url: %w", err)
	}
	if parsed.IsAbs() {
		if parsed.User != nil || !c.originAllowed(parsed) {
			return "", ErrUntrustedHost
		}
		return raw, nil
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || strings.HasPrefix(raw, "//") {
		return "", ErrUntrustedHost
	}
	return c.baseURL + "/" + strings.TrimLeft(raw, "/"), nil
}

// originAllowed reports whether an absolute URL matches the base origin.
func (c *Client) originAllowed(u *url.URL) bool {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return false
	}
	return sameOrigin(base, u)
}

// sameOrigin compares scheme, host, and effective port.
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func decode(body io.Reader, out any) error {
	if out == nil {
		_, err := io.Copy(io.Discard, body)
		return err
	}
	if err := json.NewDecoder(body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func retryAfter(resp *http.Response, attempt int) time.Duration {
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil {
			d := time.Duration(seconds) * time.Second
			return clampDelay(d)
		}
		if when, err := http.ParseTime(raw); err == nil {
			return clampDelay(time.Until(when))
		}
	}
	return clampDelay(time.Duration(attempt) * 500 * time.Millisecond)
}

func clampDelay(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// graphError carries the status and the Graph error envelope.
type graphError struct {
	Status  int
	Code    string
	Message string
}

func newGraphError(status int, body []byte) *graphError {
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	msg := payload.Error.Message
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	return &graphError{Status: status, Code: payload.Error.Code, Message: msg}
}

func (e *graphError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("graph: %d %s: %s", e.Status, e.Code, e.Message)
	case e.Message != "":
		return fmt.Sprintf("graph: %d: %s", e.Status, e.Message)
	default:
		return fmt.Sprintf("graph: status %d", e.Status)
	}
}

// Is lets callers match the shared sentinel errors.
func (e *graphError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrGone:
		return e.Status == http.StatusGone
	default:
		return false
	}
}
