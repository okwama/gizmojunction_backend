package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
)

type EmailPayload struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
}

// EmailSender mirrors supabase/functions/_shared/sendEmail.ts: a thin
// wrapper over the Resend API. If no API key is configured, sends are
// logged and skipped rather than erroring — same as the original.
type EmailSender struct {
	apiKey string
}

func NewEmailSender(apiKey string) *EmailSender {
	return &EmailSender{apiKey: apiKey}
}

func (s *EmailSender) Send(ctx context.Context, payload EmailPayload) error {
	if s.apiKey == "" {
		fmt.Printf("RESEND_API_KEY not set; email to %v skipped (subject: %s)\n", payload.To, payload.Subject)
		return nil
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("resend: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func escapeHTML(s string) string {
	return html.EscapeString(s)
}
