// Package mail sends transactional email through Resend's HTTP API.
package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Attachment struct {
	Filename string
	Content  []byte
	// ContentID makes the attachment an inline image the HTML can show
	// with <img src="cid:...">.
	ContentID string
}

type Message struct {
	To          string
	Subject     string
	HTML        string
	Text        string
	Attachments []Attachment
}

type Sender interface {
	Send(ctx context.Context, m Message) error
}

type Resend struct {
	apiKey string
	from   string
	client *http.Client
}

func NewResend(apiKey, from string) *Resend {
	return &Resend{apiKey: apiKey, from: from, client: &http.Client{Timeout: 20 * time.Second}}
}

type resendAttachment struct {
	Filename  string `json:"filename"`
	Content   string `json:"content"`
	ContentID string `json:"content_id,omitempty"`
}

func (r *Resend) Send(ctx context.Context, m Message) error {
	body := struct {
		From        string             `json:"from"`
		To          []string           `json:"to"`
		Subject     string             `json:"subject"`
		HTML        string             `json:"html"`
		Text        string             `json:"text"`
		Attachments []resendAttachment `json:"attachments,omitempty"`
	}{From: r.from, To: []string{m.To}, Subject: m.Subject, HTML: m.HTML, Text: m.Text}
	for _, a := range m.Attachments {
		body.Attachments = append(body.Attachments, resendAttachment{
			Filename: a.Filename, Content: base64.StdEncoding.EncodeToString(a.Content), ContentID: a.ContentID,
		})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("resend: %s: %s", res.Status, detail)
	}
	return nil
}
