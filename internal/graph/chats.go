package graph

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	chatsPath   = "me/chats"
	chatsTop    = 50
	messageTop  = 10
	futureClamp = 5 * time.Minute
)

// Chat is one Teams chat with its newest preview and the server-side read
// point. attention-tui never writes the read point back.
type Chat struct {
	ID            string
	Topic         string
	ChatType      string
	WebURL        string
	LastMessageAt time.Time
	LastReadAt    time.Time
}

// Message is one Teams chat message reduced to what the pane renders.
type Message struct {
	ID      string
	ChatID  string
	Sender  string
	Body    string
	WebURL  string
	Created time.Time
	Mention bool
}

type chatPage struct {
	Value    []chatJSON `json:"value"`
	NextLink string     `json:"@odata.nextLink"`
}

type chatJSON struct {
	ID                 string `json:"id"`
	Topic              string `json:"topic"`
	ChatType           string `json:"chatType"`
	WebURL             string `json:"webUrl"`
	LastMessagePreview struct {
		Created string `json:"createdDateTime"`
	} `json:"lastMessagePreview"`
	Viewpoint struct {
		LastMessageRead string `json:"lastMessageReadDateTime"`
	} `json:"viewpoint"`
}

// Chats lists chats newest-first. An empty URL starts at the first page.
func (c *Client) Chats(ctx context.Context, pageURL string) ([]Chat, string, error) {
	if strings.TrimSpace(pageURL) == "" {
		query := url.Values{}
		query.Set("$top", fmt.Sprint(chatsTop))
		query.Set("$expand", "lastMessagePreview")
		query.Set("$orderby", "lastMessagePreview/createdDateTime desc")
		pageURL = chatsPath + "?" + query.Encode()
	}

	var page chatPage
	if err := c.get(ctx, pageURL, &page); err != nil {
		return nil, "", fmt.Errorf("graph: list chats: %w", err)
	}
	chats := make([]Chat, 0, len(page.Value))
	for _, raw := range page.Value {
		chats = append(chats, raw.toChat())
	}
	return chats, page.NextLink, nil
}

func (c chatJSON) toChat() Chat {
	return Chat{
		ID:            c.ID,
		Topic:         strings.TrimSpace(c.Topic),
		ChatType:      c.ChatType,
		WebURL:        c.WebURL,
		LastMessageAt: parseGraphTime(c.LastMessagePreview.Created),
		LastReadAt:    parseGraphTime(c.Viewpoint.LastMessageRead),
	}
}

// IsUnread reports whether the newest preview is past the read point. The
// local watermark wins over the server field, and a preview far in the future
// is clamped to now so clock skew cannot pin a chat to unread forever.
func IsUnread(ch Chat, watermark, now time.Time) bool {
	read := ch.LastReadAt
	if watermark.After(read) {
		read = watermark
	}
	preview := ch.LastMessageAt
	if preview.After(now.Add(futureClamp)) {
		preview = now
	}
	return preview.After(read)
}

// ChatMessages fetches the newest messages of one chat. top is clamped to
// 1..10 because the pane only needs the newest unread one and its mention flag.
func (c *Client) ChatMessages(ctx context.Context, chatID string, top int) ([]Message, error) {
	if top <= 0 || top > messageTop {
		top = messageTop
	}
	pageURL := fmt.Sprintf("me/chats/%s/messages?$top=%d", url.PathEscape(chatID), top)

	var page struct {
		Value []messageJSON `json:"value"`
	}
	if err := c.get(ctx, pageURL, &page); err != nil {
		return nil, fmt.Errorf("graph: chat messages %s: %w", chatID, err)
	}
	msgs := make([]Message, 0, len(page.Value))
	for _, raw := range page.Value {
		msgs = append(msgs, raw.toMessage(chatID))
	}
	return msgs, nil
}

type messageJSON struct {
	ID      string `json:"id"`
	WebURL  string `json:"webUrl"`
	Created string `json:"createdDateTime"`
	Body    struct {
		Content string `json:"content"`
	} `json:"body"`
	From struct {
		User struct {
			DisplayName string `json:"displayName"`
		} `json:"user"`
		Application struct {
			DisplayName string `json:"displayName"`
		} `json:"application"`
	} `json:"from"`
	Mentions []struct {
		ID int `json:"id"`
	} `json:"mentions"`
}

func (m messageJSON) toMessage(chatID string) Message {
	sender := strings.TrimSpace(m.From.User.DisplayName)
	if sender == "" {
		sender = strings.TrimSpace(m.From.Application.DisplayName)
	}
	return Message{
		ID:      m.ID,
		ChatID:  chatID,
		Sender:  sender,
		Body:    FlattenPreview(m.Body.Content),
		WebURL:  m.WebURL,
		Created: parseGraphTime(m.Created),
		Mention: len(m.Mentions) > 0,
	}
}

// ChatMemberNames lists member display names, skipping blanks. 1:1 chats carry
// no topic, so this is how they get a readable name.
func (c *Client) ChatMemberNames(ctx context.Context, chatID string) ([]string, error) {
	pageURL := fmt.Sprintf("me/chats/%s/members", url.PathEscape(chatID))

	var page struct {
		Value []struct {
			DisplayName string `json:"displayName"`
		} `json:"value"`
	}
	if err := c.get(ctx, pageURL, &page); err != nil {
		return nil, fmt.Errorf("graph: chat members %s: %w", chatID, err)
	}
	names := make([]string, 0, len(page.Value))
	for _, m := range page.Value {
		if name := strings.TrimSpace(m.DisplayName); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// ChatName prefers the topic and falls back to the joined member names.
func ChatName(ch Chat, names []string) string {
	if topic := strings.TrimSpace(ch.Topic); topic != "" {
		return topic
	}
	return strings.Join(names, ", ")
}

func parseGraphTime(raw string) time.Time {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
