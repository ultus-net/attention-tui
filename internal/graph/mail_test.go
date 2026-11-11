package graph

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mailServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMailDeltaReturnsDeltaLink(t *testing.T) {
	body := `{
		"value": [{
			"id": "m1",
			"subject": "Q4 invoice",
			"from": {"emailAddress": {"name": "Ana Lima", "address": "ana@legal.example.com"}},
			"bodyPreview": "<p>Q4 &amp; Q3 numbers</p>",
			"webLink": "https://outlook.example/m1",
			"importance": "high",
			"receivedDateTime": "2026-01-02T03:04:05Z",
			"isRead": false,
			"inferenceClassification": "focused",
			"flag": {"flagStatus": "flagged"},
			"hasAttachments": true
		}],
		"@odata.deltaLink": "https://graph.example/delta?token=1"
	}`
	srv := mailServer(t, body)
	c := New(srv.URL, &fakeTokens{})

	res, err := c.MailDelta(context.Background(), "")
	if err != nil {
		t.Fatalf("MailDelta: %v", err)
	}
	if res.DeltaLink != "https://graph.example/delta?token=1" {
		t.Fatalf("delta link = %q", res.DeltaLink)
	}
	if res.Restart {
		t.Fatal("Restart should be false on a normal delta")
	}
	if len(res.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(res.Messages))
	}
	m := res.Messages[0]
	if m.ID != "m1" || m.Subject != "Q4 invoice" {
		t.Fatalf("message = %+v", m)
	}
	if m.Sender != "Ana Lima <ana@legal.example.com>" {
		t.Fatalf("sender = %q", m.Sender)
	}
	if m.BodyPreview != "Q4 & Q3 numbers" {
		t.Fatalf("preview = %q", m.BodyPreview)
	}
	if !m.Focused || !m.Flagged || !m.HasAttachments || m.IsRead {
		t.Fatalf("flags = %+v", m)
	}
	if got := m.Received.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-01-02T03:04:05Z" {
		t.Fatalf("received = %s", got)
	}
}

func TestMailDeltaFollowsNextLink(t *testing.T) {
	body := `{"value": [], "@odata.nextLink": "https://graph.example/page2"}`
	srv := mailServer(t, body)
	c := New(srv.URL, &fakeTokens{})

	res, err := c.MailDelta(context.Background(), "")
	if err != nil {
		t.Fatalf("MailDelta: %v", err)
	}
	if res.NextLink != "https://graph.example/page2" {
		t.Fatalf("next link = %q", res.NextLink)
	}
	if res.DeltaLink != "" {
		t.Fatalf("delta link = %q, want empty", res.DeltaLink)
	}
}

func TestMailDeltaGoneRestarts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()
	c := New(srv.URL, &fakeTokens{})

	res, err := c.MailDelta(context.Background(), "")
	if err != nil {
		t.Fatalf("MailDelta: %v", err)
	}
	if !res.Restart {
		t.Fatal("Restart should be true after a 410")
	}
}

func TestFlattenStripsTagsAndEntities(t *testing.T) {
	got := FlattenPreview("<p>Q4 &amp; Q3&nbsp;numbers</p>")
	if got != "Q4 & Q3 numbers" {
		t.Fatalf("FlattenPreview = %q, want %q", got, "Q4 & Q3 numbers")
	}
}

func TestFlattenCollapsesWhitespace(t *testing.T) {
	got := FlattenPreview("  one\ttwo\n\nthree  ")
	if got != "one two three" {
		t.Fatalf("FlattenPreview = %q", got)
	}
}

func TestMailDeltaSelectsOnlyNeededFields(t *testing.T) {
	var rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"value": []}`))
	}))
	defer srv.Close()
	c := New(srv.URL, &fakeTokens{})

	if _, err := c.MailDelta(context.Background(), ""); err != nil {
		t.Fatalf("MailDelta: %v", err)
	}
	selectValue := ""
	for _, part := range strings.Split(rawQuery, "&") {
		if strings.HasPrefix(part, "$select=") || strings.HasPrefix(part, "%24select=") {
			selectValue = part
		}
	}
	if selectValue == "" {
		t.Fatalf("query %q carries no $select", rawQuery)
	}
	for _, field := range []string{"webLink", "importance", "bodyPreview"} {
		if !strings.Contains(selectValue, field) {
			t.Fatalf("$select %q is missing %s", selectValue, field)
		}
	}
	if !strings.Contains(rawQuery, "$top=50") && !strings.Contains(rawQuery, "%24top=50") {
		t.Fatalf("query %q should cap $top at 50", rawQuery)
	}
}

func TestMailDeltaWrapsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":"AccessDenied","message":"no"}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, &fakeTokens{})
	_, err := c.MailDelta(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("err = %v, want an AccessDenied error", err)
	}
	if errors.Is(err, ErrGone) {
		t.Fatalf("err = %v, must not be ErrGone", err)
	}
}
