// Package store is the single SQLite cache shared by the poll loop, the
// interactive pane and the passive renderer.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ultus-net/attention-tui/internal/rules"
)

const schema = `
CREATE TABLE IF NOT EXISTS items (
  source      TEXT    NOT NULL,
  id          TEXT    NOT NULL,
  chat_id     TEXT    NOT NULL DEFAULT '',
  chat_name   TEXT    NOT NULL DEFAULT '',
  sender      TEXT    NOT NULL DEFAULT '',
  subject     TEXT    NOT NULL DEFAULT '',
  preview     TEXT    NOT NULL DEFAULT '',
  importance  TEXT    NOT NULL DEFAULT '',
  url         TEXT    NOT NULL DEFAULT '',
  received_at INTEGER NOT NULL,
  focused     INTEGER NOT NULL DEFAULT 0,
  flagged     INTEGER NOT NULL DEFAULT 0,
  mention     INTEGER NOT NULL DEFAULT 0,
  seen        INTEGER NOT NULL DEFAULT 0,
  first_seen  INTEGER NOT NULL,
  notified_at INTEGER,
  PRIMARY KEY (source, id)
);
CREATE INDEX IF NOT EXISTS items_received ON items (received_at DESC);
CREATE TABLE IF NOT EXISTS kv (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS chats (
  chat_id TEXT PRIMARY KEY,
  name    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS chat_watermarks (
  chat_id   TEXT PRIMARY KEY,
  watermark INTEGER NOT NULL
);
`

// Store wraps the state database.
type Store struct {
	db *sql.DB
}

// Open creates the schema when missing and returns a ready store.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: empty path")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("store: %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: create schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// UpsertItems writes items in one transaction and returns how many rows are
// new. Content columns are refreshed; seen, notified_at and first_seen are
// never overwritten, so an edited message cannot reappear or notify twice.
func (s *Store) UpsertItems(ctx context.Context, items []rules.Item) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin upsert: %w", err)
	}
	defer tx.Rollback()

	insert, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO items (
		source, id, chat_id, chat_name, sender, subject, preview, importance,
		url, received_at, focused, flagged, mention, seen, first_seen
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`)
	if err != nil {
		return 0, fmt.Errorf("store: prepare insert: %w", err)
	}
	defer insert.Close()

	update, err := tx.PrepareContext(ctx, `UPDATE items SET
		chat_id = ?, chat_name = ?, sender = ?, subject = ?, preview = ?,
		importance = ?, url = ?, received_at = ?, focused = ?, flagged = ?,
		mention = ?
		WHERE source = ? AND id = ?`)
	if err != nil {
		return 0, fmt.Errorf("store: prepare update: %w", err)
	}
	defer update.Close()

	// A newer message on the same row re-arms it: the row was read and maybe
	// notified for the previous message, not for this one.
	rearm, err := tx.PrepareContext(ctx, `UPDATE items
		SET seen = 0, notified_at = NULL, first_seen = ?
		WHERE source = ? AND id = ? AND received_at < ?`)
	if err != nil {
		return 0, fmt.Errorf("store: prepare rearm: %w", err)
	}
	defer rearm.Close()

	now := time.Now().Unix()
	inserted := 0
	for _, it := range items {
		res, err := insert.ExecContext(ctx,
			it.Source, it.ID, it.ChatID, it.ChatName, it.From, it.Subject,
			it.Preview, it.Importance, it.URL, it.Received.Unix(),
			boolInt(it.Focused), boolInt(it.Flagged), boolInt(it.Mention), now,
		)
		if err != nil {
			return 0, fmt.Errorf("store: insert %s/%s: %w", it.Source, it.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("store: insert %s/%s: %w", it.Source, it.ID, err)
		}
		if n == 0 {
			if _, err := rearm.ExecContext(ctx, now, it.Source, it.ID, it.Received.Unix()); err != nil {
				return 0, fmt.Errorf("store: rearm %s/%s: %w", it.Source, it.ID, err)
			}
			_, err = update.ExecContext(ctx,
				it.ChatID, it.ChatName, it.From, it.Subject, it.Preview,
				it.Importance, it.URL, it.Received.Unix(),
				boolInt(it.Focused), boolInt(it.Flagged), boolInt(it.Mention),
				it.Source, it.ID,
			)
			if err != nil {
				return 0, fmt.Errorf("store: update %s/%s: %w", it.Source, it.ID, err)
			}
		}
		inserted += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit upsert: %w", err)
	}
	return inserted, nil
}

// Items returns every unseen row, newest first.
func (s *Store) Items(ctx context.Context) ([]rules.Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		source, id, chat_id, chat_name, sender, subject, preview, importance,
		url, received_at, focused, flagged, mention, seen
		FROM items WHERE seen = 0 ORDER BY received_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list items: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// SetSeen marks one row read. Seen is local only; nothing is written to Graph.
func (s *Store) SetSeen(ctx context.Context, source, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE items SET seen = 1 WHERE source = ? AND id = ?`, source, id)
	if err != nil {
		return fmt.Errorf("store: set seen %s/%s: %w", source, id, err)
	}
	return nil
}

// PendingNotifications returns unseen rows first seen inside window that have
// never notified. It only selects; the caller decides which are alert-tier and
// stamps them with MarkNotified.
func (s *Store) PendingNotifications(ctx context.Context, window time.Duration) ([]rules.Item, error) {
	cutoff := time.Now().Add(-window).Unix()

	rows, err := s.db.QueryContext(ctx, `SELECT
		source, id, chat_id, chat_name, sender, subject, preview, importance,
		url, received_at, focused, flagged, mention, seen
		FROM items
		WHERE seen = 0 AND notified_at IS NULL AND first_seen >= ?
		ORDER BY first_seen`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("store: list notifications: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// MarkNotified stamps rows as notified in one transaction, so a crash cannot
// double-notify the same item.
func (s *Store) MarkNotified(ctx context.Context, items []rules.Item) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin mark notified: %w", err)
	}
	defer tx.Rollback()
	stamp, err := tx.PrepareContext(ctx, `UPDATE items SET notified_at = ? WHERE source = ? AND id = ?`)
	if err != nil {
		return fmt.Errorf("store: prepare notify stamp: %w", err)
	}
	defer stamp.Close()
	now := time.Now().Unix()
	for _, it := range items {
		if _, err := stamp.ExecContext(ctx, now, it.Source, it.ID); err != nil {
			return fmt.Errorf("store: stamp %s/%s: %w", it.Source, it.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit mark notified: %w", err)
	}
	return nil
}

// Prune drops rows older than olderThan, then caps the table at max rows.
func (s *Store) Prune(ctx context.Context, olderThan time.Time, max int) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin prune: %w", err)
	}
	defer tx.Rollback()

	deleted := 0
	res, err := tx.ExecContext(ctx, `DELETE FROM items WHERE received_at < ?`, olderThan.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: prune by age: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune by age: %w", err)
	}
	deleted += int(n)

	if max > 0 {
		res, err = tx.ExecContext(ctx, `DELETE FROM items WHERE rowid NOT IN (
			SELECT rowid FROM items ORDER BY received_at DESC LIMIT ?
		)`, max)
		if err != nil {
			return 0, fmt.Errorf("store: prune by count: %w", err)
		}
		n, err = res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("store: prune by count: %w", err)
		}
		deleted += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit prune: %w", err)
	}
	return deleted, nil
}

// KV reads one opaque value, such as the mail delta link.
func (s *Store) KV(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: kv %s: %w", key, err)
	}
	return value, true, nil
}

// SetKV writes one opaque value.
func (s *Store) SetKV(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO kv (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: set kv %s: %w", key, err)
	}
	return nil
}

// ChatName reads a cached chat display name.
func (s *Store) ChatName(ctx context.Context, chatID string) (string, bool, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM chats WHERE chat_id = ?`, chatID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: chat name %s: %w", chatID, err)
	}
	return name, true, nil
}

// SetChatName caches a chat display name.
func (s *Store) SetChatName(ctx context.Context, chatID, name string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO chats (chat_id, name) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET name = excluded.name`, chatID, name)
	if err != nil {
		return fmt.Errorf("store: set chat name %s: %w", chatID, err)
	}
	return nil
}

// ChatWatermark reads the local point up to which a chat has been seen.
func (s *Store) ChatWatermark(ctx context.Context, chatID string) (time.Time, error) {
	var unix int64
	err := s.db.QueryRowContext(ctx, `SELECT watermark FROM chat_watermarks WHERE chat_id = ?`, chatID).Scan(&unix)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("store: chat watermark %s: %w", chatID, err)
	}
	return time.Unix(unix, 0).UTC(), nil
}

// SetChatWatermark records the local point up to which a chat has been seen.
func (s *Store) SetChatWatermark(ctx context.Context, chatID string, t time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_watermarks (chat_id, watermark) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET watermark = excluded.watermark`, chatID, t.Unix())
	if err != nil {
		return fmt.Errorf("store: set chat watermark %s: %w", chatID, err)
	}
	return nil
}

func scanItems(rows *sql.Rows) ([]rules.Item, error) {
	var items []rules.Item
	for rows.Next() {
		var (
			it                        rules.Item
			received, seen            int64
			focused, flagged, mention int64
		)
		if err := rows.Scan(
			&it.Source, &it.ID, &it.ChatID, &it.ChatName, &it.From,
			&it.Subject, &it.Preview, &it.Importance, &it.URL, &received,
			&focused, &flagged, &mention, &seen,
		); err != nil {
			return nil, fmt.Errorf("store: scan item: %w", err)
		}
		it.Received = time.Unix(received, 0).UTC()
		it.Focused = focused != 0
		it.Flagged = flagged != 0
		it.Mention = mention != 0
		it.Seen = seen != 0
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate items: %w", err)
	}
	return items, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
