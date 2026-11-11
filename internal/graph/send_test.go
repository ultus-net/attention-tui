package graph

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendChatMessagePostsText(t *testing.T) {
	var method, path, auth string
	var payload struct {
		Body struct {
			ContentType string `json:"contentType"`
			Content     string `json:"content"`
		} `json:"body"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"sent"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, &fakeTokens{})
	if err := c.SendChatMessage(context.Background(), "19:abc", "on it"); err != nil {
		t.Fatalf("SendChatMessage: %v", err)
	}
	if method != http.MethodPost {
		t.Fatalf("method = %s, want POST", method)
	}
	if path != "/me/chats/19:abc/messages" {
		t.Fatalf("path = %q", path)
	}
	if auth != "Bearer token-1" {
		t.Fatalf("Authorization = %q", auth)
	}
	if payload.Body.Content != "on it" || payload.Body.ContentType != "text" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestSendChatMessageRejectsEmptyText(t *testing.T) {
	c := New("http://127.0.0.1:1", &fakeTokens{})
	if err := c.SendChatMessage(context.Background(), "19:abc", "   "); err == nil {
		t.Fatal("expected empty text to be rejected")
	}
}
