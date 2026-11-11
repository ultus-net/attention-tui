package graph

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"
)

const (
	mailDeltaPath = "me/mailFolders/inbox/messages/delta"
	mailTop       = 50
	mailSelect    = "id,subject,from,bodyPreview,webLink,importance,receivedDateTime,isRead,inferenceClassification,flag,hasAttachments"
)

// MailMessage is one inbox message reduced to the fields the pane renders.
type MailMessage struct {
	ID             string
	Sender         string
	Subject        string
	BodyPreview    string
	WebLink        string
	Importance     string
	Received       time.Time
	Focused        bool
	Flagged        bool
	IsRead         bool
	HasAttachments bool
}

// MailDeltaResult is one page of a delta walk.
type MailDeltaResult struct {
	Messages  []MailMessage
	NextLink  string
	DeltaLink string
	// Restart is set when the delta link expired, so the caller must begin a
	// fresh bounded walk from the inbox root.
	Restart bool
}

type mailPage struct {
	Value     []mailMessage `json:"value"`
	NextLink  string        `json:"@odata.nextLink"`
	DeltaLink string        `json:"@odata.deltaLink"`
}

type mailMessage struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	BodyPreview string `json:"bodyPreview"`
	WebLink     string `json:"webLink"`
	Importance  string `json:"importance"`
	Received    string `json:"receivedDateTime"`
	IsRead      bool   `json:"isRead"`
	Focused     string `json:"inferenceClassification"`
	HasAttach   bool   `json:"hasAttachments"`
	From        struct {
		EmailAddress struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from"`
	Flag struct {
		Status string `json:"flagStatus"`
	} `json:"flag"`
}

// MailDelta fetches one page of the inbox delta. An empty URL starts at the
// inbox root. An expired delta returns Restart with a nil error.
func (c *Client) MailDelta(ctx context.Context, pageURL string) (MailDeltaResult, error) {
	if strings.TrimSpace(pageURL) == "" {
		query := url.Values{}
		query.Set("$top", fmt.Sprint(mailTop))
		query.Set("$select", mailSelect)
		pageURL = mailDeltaPath + "?" + query.Encode()
	}

	var page mailPage
	if err := c.get(ctx, pageURL, &page); err != nil {
		if errors.Is(err, ErrGone) {
			return MailDeltaResult{Restart: true}, nil
		}
		return MailDeltaResult{}, fmt.Errorf("graph: mail delta: %w", err)
	}

	res := MailDeltaResult{
		Messages:  make([]MailMessage, 0, len(page.Value)),
		NextLink:  page.NextLink,
		DeltaLink: page.DeltaLink,
	}
	for _, m := range page.Value {
		res.Messages = append(res.Messages, m.toMessage())
	}
	return res, nil
}

func (m mailMessage) toMessage() MailMessage {
	received, _ := time.Parse(time.RFC3339, m.Received)
	return MailMessage{
		ID:             m.ID,
		Sender:         formatSender(m.From.EmailAddress.Name, m.From.EmailAddress.Address),
		Subject:        strings.TrimSpace(m.Subject),
		BodyPreview:    FlattenPreview(m.BodyPreview),
		WebLink:        m.WebLink,
		Importance:     strings.ToLower(strings.TrimSpace(m.Importance)),
		Received:       received.UTC(),
		Focused:        strings.EqualFold(m.Focused, "focused"),
		Flagged:        strings.EqualFold(m.Flag.Status, "flagged"),
		IsRead:         m.IsRead,
		HasAttachments: m.HasAttach,
	}
}

func formatSender(name, address string) string {
	name = strings.TrimSpace(name)
	address = strings.TrimSpace(address)
	switch {
	case name != "" && address != "":
		return name + " <" + address + ">"
	case address != "":
		return address
	default:
		return name
	}
}

// FlattenPreview turns bodyPreview into one plain line: entities decoded, tags
// dropped, whitespace collapsed.
func FlattenPreview(s string) string {
	decoded := html.UnescapeString(s)
	var b strings.Builder
	b.Grow(len(decoded))
	inTag := false
	for _, r := range decoded {
		switch {
		case r == '<':
			if !inTag {
				inTag = true
				b.WriteRune(' ')
			}
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
