// Package ai ports the two on-demand AI utility Edge Functions
// (generate-blog, suggest-name). These aren't background jobs — the
// originals are synchronous request/response endpoints called from the
// admin UI — so they're plain HTTP handlers here, not river workers.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

type Config struct {
	GeminiAPIKey string
	GroqAPIKey   string
}

var codeFenceRE = regexp.MustCompile("```json\\n?|\\n?```")

func stripCodeFence(raw string) string {
	return strings.TrimSpace(codeFenceRE.ReplaceAllString(raw, ""))
}

func postJSON(ctx context.Context, url string, headers map[string]string, body any) (map[string]any, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

func getPath(m map[string]any, path ...string) (any, bool) {
	var cur any = m
	for _, p := range path {
		switch t := cur.(type) {
		case map[string]any:
			v, ok := t[p]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			idx := 0
			if _, err := fmt.Sscanf(p, "%d", &idx); err != nil || idx >= len(t) {
				return nil, false
			}
			cur = t[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

func getString(m map[string]any, path ...string) (string, bool) {
	v, ok := getPath(m, path...)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}
