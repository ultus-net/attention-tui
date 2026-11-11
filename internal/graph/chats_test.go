package graph

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func jsonServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestChatsParsesPreviewAndReadTime(t *testing.T) {
	body := `{"value":[{
		"id": "19:abc",
		"topic": "Deploy Bot",
		"chatType": "group",
		"webUrl": "https://teams.example/19:abc",
		"lastMessagePreview": {"createdDateTime": "2026-01-02T10:00:00Z", "body": {"content": "build failed"}},
		"viewpoint": {"lastMessageReadDateTime": "2026-01-02T09:00:00Z"}
	}]}`
	srv := jsonServer(t, body)
	c := New(srv.URL, &fakeTokens{})

	chats, next, err := c.Chats(context.Background(), "")
	if err != nil {
		t.Fatalf("Chats: %v", err)
	}
	if next != "" {
		t.Fatalf("next = %q, want empty", next)
	}
	if len(chats) != 1 {
		t.Fatalf("chats = %d, want 1", len(chats))
	}
	ch := chats[0]
	if ch.ID != "19:abc" || ch.Topic != "Deploy Bot" || ch.ChatType != "group" {
		t.Fatalf("chat = %+v", ch)
	}
	if !ch.LastMessageAt.Equal(time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("last message = %s", ch.LastMessageAt)
	}
	if !ch.LastReadAt.Equal(time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("last read = %s", ch.LastReadAt)
	}
}

func TestChatsReturnsNextLink(t *testing.T) {
	body := `{"value": [], "@odata.nextLink": "https://graph.example/chats/page2"}`
	srv := jsonServer(t, body)
	c := New(srv.URL, &fakeTokens{})

	_, next, err := c.Chats(context.Background(), "")
	if err != nil {
		t.Fatalf("Chats: %v", err)
	}
	if next != "https://graph.example/chats/page2" {
		t.Fatalf("next = %q", next)
	}
}

func TestIsUnreadComparesPreviewToRead(t *testing.T) {
	preview := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	read := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	now := time.Date(2026, 1, 2, 11, 0, 0, 0, time.UTC)
	ch := Chat{LastMessageAt: preview, LastReadAt: read}
	if !IsUnread(ch, time.Time{}, now) {
		t.Fatal("a message newer than the read time is unread")
	}
	ch.LastReadAt = time.Date(2026, 1, 2, 10, 5, 0, 0, time.UTC)
	if IsUnread(ch, time.Time{}, now) {
		t.Fatal("a message older than the read time is read")
	}
}

func TestLocalWatermarkSuppressesFuturePreview(t *testing.T) {
	now := time.Date(2026, 1, 2, 11, 0, 0, 0, time.UTC)
	ch := Chat{
		LastMessageAt: now.Add(2 * time.Hour),
		LastReadAt:    time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
	}
	if IsUnread(ch, now, now) {
		t.Fatal("clock-skewed future preview must not pin the chat to unread")
	}
}

func TestChatMessagesMentionFlag(t *testing.T) {
	body := `{"value":[{
		"id": "msg1",
		"from": {"user": {"displayName": "Ana Lima"}},
		"body": {"contentType": "html", "content": "<p>build failed</p>"},
		"webUrl": "https://teams.example/msg1",
		"createdDateTime": "2026-01-02T10:00:00Z",
		"mentions": [{"id": 0, "mentionText": "you"}]
	},{
		"id": "msg2",
		"from": {"user": {"displayName": "Ben"}},
		"body": {"contentType": "html", "content": "ok"},
		"webUrl": "https://teams.example/msg2",
		"createdDateTime": "2026-01-02T10:01:00Z"
	}]}`
	srv := jsonServer(t, body)
	c := New(srv.URL, &fakeTokens{})

	msgs, err := c.ChatMessages(context.Background(), "19:abc", 0)
	if err != nil {
		t.Fatalf("ChatMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	if !msgs[0].Mention {
		t.Fatal("msg1 carries a mentions array and must be flagged")
	}
	if msgs[1].Mention {
		t.Fatal("msg2 has no mentions")
	}
	if msgs[0].Sender != "Ana Lima" {
		t.Fatalf("sender = %q", msgs[0].Sender)
	}
	if msgs[0].Body != "build failed" {
		t.Fatalf("body = %q", msgs[0].Body)
	}
	if msgs[0].ChatID != "19:abc" {
		t.Fatalf("chat id = %q", msgs[0].ChatID)
	}
}

func TestChatMessagesDefaultTop(t *testing.T) {
	var rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"value": []}`))
	}))
	defer srv.Close()
	c := New(srv.URL, &fakeTokens{})

	if _, err := c.ChatMessages(context.Background(), "19:abc", 0); err != nil {
		t.Fatalf("ChatMessages: %v", err)
	}
	if rawQuery != "$top=10" {
		t.Fatalf("query = %q, want $top=10", rawQuery)
	}
	if _, err := c.ChatMessages(context.Background(), "19:abc", 99); err != nil {
		t.Fatalf("ChatMessages: %v", err)
	}
	if rawQuery != "$top=10" {
		t.Fatalf("query = %q, want a hard cap of 10", rawQuery)
	}
}

func TestMemberNamesSkipEmpty(t *testing.T) {
	body := `{"value": [{"displayName": "Ana"}, {"displayName": ""}, {"displayName": "Ben"}]}`
	srv := jsonServer(t, body)
	c := New(srv.URL, &fakeTokens{})

	names, err := c.ChatMemberNames(context.Background(), "19:abc")
	if err != nil {
		t.Fatalf("ChatMemberNames: %v", err)
	}
	if len(names) != 2 || names[0] != "Ana" || names[1] != "Ben" {
		t.Fatalf("names = %v", names)
	}
}

func TestChatNameFromTopicElseMembers(t *testing.T) {
	if got := ChatName(Chat{Topic: "Deploy Bot"}, []string{"Ana", "Ben"}); got != "Deploy Bot" {
		t.Fatalf("name = %q, want the topic", got)
	}
	if got := ChatName(Chat{}, []string{"Ana", "Ben"}); got != "Ana, Ben" {
		t.Fatalf("name = %q, want the joined members", got)
	}
}
