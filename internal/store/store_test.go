package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ultus-net/attention-tui/internal/rules"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func mailItem(id, preview string) rules.Item {
	return rules.Item{
		Source:     "mail",
		ID:         id,
		From:       "Ana <ana@example.com>",
		Subject:    "Subject " + id,
		Preview:    preview,
		Importance: "normal",
		URL:        "https://outlook.example/" + id,
		Received:   time.Unix(1_700_000_000, 0).UTC(),
	}
}

func chatItem(id string) rules.Item {
	return rules.Item{
		Source:   "chat",
		ID:       id,
		ChatID:   "19:" + id,
		ChatName: "Deploy Bot",
		From:     "Deploy Bot",
		Preview:  "build failed",
		URL:      "https://teams.example/" + id,
		Received: time.Unix(1_700_000_100, 0).UTC(),
	}
}

func TestUpsertIsIdempotent(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	it := mailItem("m1", "hello")

	inserted, err := s.UpsertItems(ctx, []rules.Item{it})
	if err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want 1", inserted)
	}
	inserted, err = s.UpsertItems(ctx, []rules.Item{it})
	if err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("inserted = %d, want 0 on the second write", inserted)
	}
	items, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
}

func TestEditKeepsSeenAndNotified(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	it := mailItem("m1", "original")
	if _, err := s.UpsertItems(ctx, []rules.Item{it}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	pending, err := s.PendingNotifications(ctx, time.Hour)
	if err != nil {
		t.Fatalf("PendingNotifications: %v", err)
	}
	if err := s.MarkNotified(ctx, pending); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}
	if err := s.SetSeen(ctx, it.Source, it.ID); err != nil {
		t.Fatalf("SetSeen: %v", err)
	}

	it.Preview = "edited"
	if _, err := s.UpsertItems(ctx, []rules.Item{it}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}

	var seen int
	var notified *int64
	row := s.db.QueryRow(`SELECT seen, notified_at FROM items WHERE source = ? AND id = ?`, it.Source, it.ID)
	if err := row.Scan(&seen, &notified); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if seen != 1 {
		t.Fatalf("seen = %d, want 1 after an edit", seen)
	}
	if notified == nil {
		t.Fatal("notified_at was cleared by an edit, so the item could notify twice")
	}
	items, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %d, want 0: an edited seen row must not reappear", len(items))
	}
}

func TestSeenFilters(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	if _, err := s.UpsertItems(ctx, []rules.Item{mailItem("m1", "a"), mailItem("m2", "b")}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	if err := s.SetSeen(ctx, "mail", "m1"); err != nil {
		t.Fatalf("SetSeen: %v", err)
	}
	items, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 1 || items[0].ID != "m2" {
		t.Fatalf("items = %+v, want only m2", items)
	}
	if items[0].Seen {
		t.Fatalf("unseen rows must not report Seen")
	}
}

func TestPendingNotificationsOnce(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	if _, err := s.UpsertItems(ctx, []rules.Item{mailItem("m1", "a")}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	first, err := s.PendingNotifications(ctx, time.Hour)
	if err != nil {
		t.Fatalf("PendingNotifications: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first call = %d items, want 1", len(first))
	}
	again, err := s.PendingNotifications(ctx, time.Hour)
	if err != nil {
		t.Fatalf("PendingNotifications: %v", err)
	}
	if len(again) != 1 {
		t.Fatalf("selection alone must not stamp: got %d items", len(again))
	}
	if err := s.MarkNotified(ctx, first); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}
	second, err := s.PendingNotifications(ctx, time.Hour)
	if err != nil {
		t.Fatalf("PendingNotifications: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("after MarkNotified = %d items, want 0", len(second))
	}
}

func TestNewerMessageRearms(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	it := chatItem("c1")
	if _, err := s.UpsertItems(ctx, []rules.Item{it}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	pending, err := s.PendingNotifications(ctx, time.Hour)
	if err != nil {
		t.Fatalf("PendingNotifications: %v", err)
	}
	if err := s.MarkNotified(ctx, pending); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}
	if err := s.SetSeen(ctx, it.Source, it.ID); err != nil {
		t.Fatalf("SetSeen: %v", err)
	}

	newer := it
	newer.Preview = "new message"
	newer.Received = it.Received.Add(time.Minute)
	if _, err := s.UpsertItems(ctx, []rules.Item{newer}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}

	items, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want the chat to reappear as unseen", len(items))
	}
	rearmed, err := s.PendingNotifications(ctx, time.Hour)
	if err != nil {
		t.Fatalf("PendingNotifications: %v", err)
	}
	if len(rearmed) != 1 {
		t.Fatalf("pending = %d, want the newer message to be notify-eligible again", len(rearmed))
	}
}

func TestPendingNotificationsWindow(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	it := mailItem("m1", "old")
	if _, err := s.UpsertItems(ctx, []rules.Item{it}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	old := time.Now().Add(-3 * time.Minute).Unix()
	if _, err := s.db.Exec(`UPDATE items SET first_seen = ?`, old); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	got, err := s.PendingNotifications(ctx, 2*time.Minute)
	if err != nil {
		t.Fatalf("PendingNotifications: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d items, want 0 outside the window", len(got))
	}
}

func TestPrune(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	items := make([]rules.Item, 0, 600)
	base := time.Unix(1_700_000_000, 0).UTC()
	for i := 0; i < 600; i++ {
		it := mailItem("m", "preview")
		it.ID = "m" + time.Duration(i).String()
		it.Received = base.Add(time.Duration(i) * time.Second)
		items = append(items, it)
	}
	if _, err := s.UpsertItems(ctx, items); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}
	deleted, err := s.Prune(ctx, base, 500)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 100 {
		t.Fatalf("deleted = %d, want 100", deleted)
	}
	remaining, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(remaining) != 500 {
		t.Fatalf("remaining = %d, want 500", len(remaining))
	}
}

func TestKVRoundTrip(t *testing.T) {
	s, path := open(t)
	ctx := context.Background()
	if err := s.SetKV(ctx, "mail_delta", "https://graph.example/delta?token=1"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reopened.Close()
	value, ok, err := reopened.KV(ctx, "mail_delta")
	if err != nil {
		t.Fatalf("KV: %v", err)
	}
	if !ok || value != "https://graph.example/delta?token=1" {
		t.Fatalf("KV = %q, %v", value, ok)
	}
}

func TestChatWatermark(t *testing.T) {
	s, path := open(t)
	ctx := context.Background()
	want := time.Unix(1_700_000_500, 0).UTC()
	if err := s.SetChatWatermark(ctx, "19:abc", want); err != nil {
		t.Fatalf("SetChatWatermark: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.ChatWatermark(ctx, "19:abc")
	if err != nil {
		t.Fatalf("ChatWatermark: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("watermark = %s, want %s", got, want)
	}
	missing, err := reopened.ChatWatermark(ctx, "19:missing")
	if err != nil {
		t.Fatalf("ChatWatermark: %v", err)
	}
	if !missing.IsZero() {
		t.Fatalf("missing watermark = %s, want zero", missing)
	}
}

func TestDeleteItemsRemovesOnlyMatchingRows(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	if _, err := s.UpsertItems(ctx, []rules.Item{mailItem("m1", "one"), mailItem("m2", "two"), chatItem("c1")}); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}

	deleted, err := s.DeleteItems(ctx, "mail", []string{"m1", "m1", "missing"})
	if err != nil {
		t.Fatalf("DeleteItems: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 and no repeat work on a duplicate id", deleted)
	}
	items, err := s.Items(ctx)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want m2 and c1", items)
	}
	for _, it := range items {
		if it.Source == "mail" && it.ID == "m1" {
			t.Fatal("m1 survived the delete")
		}
	}
}

func TestChatName(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	if _, ok, err := s.ChatName(ctx, "19:abc"); err != nil || ok {
		t.Fatalf("ChatName = _, %v, %v; want not found", ok, err)
	}
	if err := s.SetChatName(ctx, "19:abc", "Ana, Ben"); err != nil {
		t.Fatalf("SetChatName: %v", err)
	}
	name, ok, err := s.ChatName(ctx, "19:abc")
	if err != nil {
		t.Fatalf("ChatName: %v", err)
	}
	if !ok || name != "Ana, Ben" {
		t.Fatalf("ChatName = %q, %v", name, ok)
	}
}

func TestOpenStampsSchemaVersion(t *testing.T) {
	s, _ := open(t)
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); err != nil {
		db.Close()
		t.Fatalf("set user_version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := Open(path)
	if err == nil {
		s.Close()
		t.Fatal("Open accepted a database written by a newer build")
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Fatalf("err = %v, want a newer-schema explanation", err)
	}
}
