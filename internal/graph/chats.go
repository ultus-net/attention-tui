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
	// Members carries expanded member display names with the signed-in user
	// removed. It is empty when the walk did not ask for the expansion.
	Members []string
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
	Members []struct {
		UserID      string `json:"userId"`
		DisplayName string `json:"displayName"`
	} `json:"members"`
}

// Chats lists chats newest-first. An empty URL starts at the first page.
// expandMembers asks for the member list in the same response. Graph caps that
// expansion at 25 members with no continuation, so a larger group chat is
// named from the first 25. The per-chat member call remains the fallback when
// the expansion is absent.
func (c *Client) Chats(ctx context.Context, pageURL string, expandMembers bool) ([]Chat, string, error) {
	if strings.TrimSpace(pageURL) == "" {
		query := url.Values{}
		query.Set("$top", fmt.Sprint(chatsTop))
		expand := "lastMessagePreview"
		if expandMembers {
			expand += ",members"
		}
		query.Set("$expand", expand)
		query.Set("$orderby", "lastMessagePreview/createdDateTime desc")
		pageURL = chatsPath + "?" + query.Encode()
	}

	var page chatPage
	if err := c.get(ctx, pageURL, &page); err != nil {
		return nil, "", fmt.Errorf("graph: list chats: %w", err)
	}

	self := ""
	if hasMembers(page.Value) {
		// One /me call names every chat on the page. A failure fails the walk,
		// because an unfiltered list would name the chat "you, Ana".
		id, err := c.signedInUserID(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("graph: list chats: %w", err)
		}
		self = id
	}

	chats := make([]Chat, 0, len(page.Value))
	for _, raw := range page.Value {
		chats = append(chats, raw.toChat(self))
	}
	return chats, page.NextLink, nil
}

func hasMembers(chats []chatJSON) bool {
	for _, ch := range chats {
		if len(ch.Members) > 0 {
			return true
		}
	}
	return false
}

func (c chatJSON) toChat(self string) Chat {
	members := make([]string, 0, len(c.Members))
	for _, m := range c.Members {
		if self != "" && m.UserID == self {
			continue
		}
		if name := strings.TrimSpace(m.DisplayName); name != "" {
			members = append(members, name)
		}
	}
	return Chat{
		ID:            c.ID,
		Topic:         strings.TrimSpace(c.Topic),
		ChatType:      c.ChatType,
		WebURL:        c.WebURL,
		LastMessageAt: parseGraphTime(c.LastMessagePreview.Created),
		LastReadAt:    parseGraphTime(c.Viewpoint.LastMessageRead),
		Members:       members,
	}
}

// IsUnread reports whether the newest preview is past the read point, and
// whether that can be known at all. A chat with no preview has no message to
// compare, so the caller must skip it rather than call it read. The local
// watermark wins over the server field, and a preview far in the future is
// clamped to now so clock skew cannot pin a chat to unread forever.
func IsUnread(ch Chat, watermark, now time.Time) (unread, known bool) {
	preview := ch.LastMessageAt
	if preview.IsZero() {
		return false, false
	}
	if preview.After(now.Add(futureClamp)) {
		preview = now
	}
	read := ch.LastReadAt
	if watermark.After(read) {
		read = watermark
	}
	return preview.After(read), true
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

// User is the signed-in account, reduced to what chat naming needs.
type User struct {
	ID          string
	DisplayName string
}

// Me fetches the signed-in user.
func (c *Client) Me(ctx context.Context) (User, error) {
	var raw struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
	}
	if err := c.get(ctx, "me?$select=id,displayName", &raw); err != nil {
		return User{}, fmt.Errorf("graph: me: %w", err)
	}
	return User{ID: raw.ID, DisplayName: strings.TrimSpace(raw.DisplayName)}, nil
}

// signedInUserID caches the caller's object id. A failure is not cached, so a
// transient network error cannot poison naming until restart.
func (c *Client) signedInUserID(ctx context.Context) (string, error) {
	c.selfMu.Lock()
	defer c.selfMu.Unlock()
	if c.selfUserID != "" {
		return c.selfUserID, nil
	}
	me, err := c.Me(ctx)
	if err != nil {
		return "", err
	}
	c.selfUserID = me.ID
	return c.selfUserID, nil
}

// ChatMemberNames lists member display names, skipping blanks and the
// signed-in user. 1:1 chats carry no topic, so this is how they get the other
// participant's name rather than "you, them".
func (c *Client) ChatMemberNames(ctx context.Context, chatID string) ([]string, error) {
	self, err := c.signedInUserID(ctx)
	if err != nil {
		return nil, err
	}
	pageURL := fmt.Sprintf("me/chats/%s/members", url.PathEscape(chatID))

	var page struct {
		Value []struct {
			UserID      string `json:"userId"`
			DisplayName string `json:"displayName"`
		} `json:"value"`
	}
	if err := c.get(ctx, pageURL, &page); err != nil {
		return nil, fmt.Errorf("graph: chat members %s: %w", chatID, err)
	}
	names := make([]string, 0, len(page.Value))
	for _, m := range page.Value {
		if self != "" && m.UserID == self {
			continue
		}
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
