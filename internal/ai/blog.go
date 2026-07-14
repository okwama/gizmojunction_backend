package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"gizmojunction/backend/internal/auth"
)

func callAI(ctx context.Context, cfg Config, prompt string) (string, error) {
	if cfg.GeminiAPIKey != "" {
		body := map[string]any{
			"contents":         []map[string]any{{"parts": []map[string]any{{"text": prompt}}}},
			"generationConfig": map[string]any{"responseMimeType": "application/json"},
		}
		url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent?key=%s", cfg.GeminiAPIKey)
		if resp, err := postJSON(ctx, url, nil, body); err == nil {
			if _, hasErr := resp["error"]; !hasErr {
				if text, ok := getString(resp, "candidates", "0", "content", "parts", "0", "text"); ok {
					return strings.TrimSpace(text), nil
				}
			}
		}
	}

	if cfg.GroqAPIKey != "" {
		body := map[string]any{
			"model":       "llama-3.1-8b-instant",
			"temperature": 0.7,
			"messages": []map[string]string{
				{"role": "system", "content": "You are a professional tech blog writer for GizmoJunction, a Kenyan technology retail store. Always respond with valid JSON only — no markdown, no code fences."},
				{"role": "user", "content": prompt},
			},
		}
		headers := map[string]string{"Authorization": "Bearer " + cfg.GroqAPIKey}
		if resp, err := postJSON(ctx, "https://api.groq.com/openai/v1/chat/completions", headers, body); err == nil {
			if _, hasErr := resp["error"]; !hasErr {
				if text, ok := getString(resp, "choices", "0", "message", "content"); ok {
					return strings.TrimSpace(text), nil
				}
			}
		}
	}

	return "", fmt.Errorf("AI generation failed: Gemini and Groq both unavailable or errored")
}

type GenerateBlogInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Mode       string          `json:"mode" doc:"draft, ideas, or product"`
		Topic      string          `json:"topic,omitempty"`
		Keywords   string          `json:"keywords,omitempty"`
		Product    json.RawMessage `json:"product,omitempty"`
		Categories []string        `json:"categories,omitempty"`
	}
}

type GenerateBlogOutput struct {
	Body struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
}

func RegisterGenerateBlog(api huma.API, cfg Config, authSvc *auth.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "generate-blog",
		Method:      "POST",
		Path:        "/v1/admin/ai/generate-blog",
		Summary:     "AI-assisted blog draft/ideas/product-spotlight generation",
	}, func(ctx context.Context, input *GenerateBlogInput) (*GenerateBlogOutput, error) {
		if _, err := authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
			return nil, err
		}

		var prompt string
		switch input.Body.Mode {
		case "draft":
			prompt = fmt.Sprintf(`You are a professional tech blog writer for GizmoJunction, a Kenyan technology retail store. Write an engaging, SEO-optimized blog post.

Topic: "%s"
%s

Return ONLY a JSON object with this exact structure:
{
  "title": "Compelling SEO-friendly title (under 60 chars)",
  "slug": "url-friendly-slug-with-hyphens",
  "excerpt": "A 2-sentence meta description for SEO and blog listing cards (under 160 chars)",
  "content": "Full HTML blog post body with <h2>, <h3>, <p>, <ul>, <li> tags. Minimum 600 words. Include practical tips, product recommendations, and Kenyan market context where relevant. Do NOT include <html>, <body>, or <head> tags."
}`, input.Body.Topic, keywordsLine(input.Body.Keywords))

		case "ideas":
			categoriesJSON, _ := json.Marshal(input.Body.Categories)
			prompt = fmt.Sprintf(`You are an SEO and content strategy expert for GizmoJunction, a Kenyan tech retail store.

The store sells products in these categories: %s

Generate 10 highly targeted, SEO-friendly blog post ideas. Focus on what Kenyan tech buyers search for online.

Return ONLY a JSON array:
[
  {
    "title": "Blog post title",
    "topic": "2-sentence description of what to cover in this post",
    "keywords": "comma-separated target keywords",
    "category": "which product category this supports",
    "why": "One sentence on why this drives traffic or sales"
  }
]`, categoriesJSON)

		case "product":
			prompt = fmt.Sprintf(`You are a professional tech blog writer for GizmoJunction, a Kenyan technology retail store.

Write an engaging product spotlight / review blog post for this product:
%s

Guidelines:
- Write for a Kenyan audience
- Cover: what it is, key features and specs, who it's ideal for, value for money in Kenya
- Include a call-to-action mentioning GizmoJunction
- Use proper HTML: <h2>, <h3>, <p>, <ul>, <li>

Return ONLY a JSON object:
{
  "title": "Engaging product spotlight title",
  "slug": "url-slug",
  "excerpt": "2-sentence blog card excerpt",
  "content": "Full HTML blog post body, minimum 400 words. No <html>/<body>/<head> tags."
}`, input.Body.Product)

		default:
			return nil, huma.Error400BadRequest("Invalid mode. Use: draft, ideas, or product")
		}

		raw, err := callAI(ctx, cfg, prompt)
		if err != nil {
			return nil, huma.Error500InternalServerError(err.Error())
		}

		clean := stripCodeFence(raw)
		if !json.Valid([]byte(clean)) {
			return nil, huma.Error500InternalServerError("AI returned invalid JSON: " + raw)
		}

		out := &GenerateBlogOutput{}
		out.Body.Success = true
		out.Body.Data = json.RawMessage(clean)
		return out, nil
	})
}

func keywordsLine(keywords string) string {
	if keywords == "" {
		return ""
	}
	return "Target keywords: " + keywords
}
