package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"gizmojunction/backend/internal/auth"
)

func callGeminiForName(ctx context.Context, apiKey, prompt string) (string, error) {
	if apiKey == "" {
		return "", nil
	}
	body := map[string]any{
		"contents": []map[string]any{{"parts": []map[string]any{{"text": prompt}}}},
		"generationConfig": map[string]any{
			"responseMimeType": "application/json",
			"responseSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"suggestedName":         map[string]any{"type": "string"},
					"suggestedRootCategory": map[string]any{"type": "string"},
					"suggestedSubCategory":  map[string]any{"type": "string"},
					"suggestedSummary":      map[string]any{"type": "string"},
					"suggestedDescription":  map[string]any{"type": "string"},
				},
				"required": []string{"suggestedName", "suggestedRootCategory", "suggestedSubCategory"},
			},
		},
	}
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key=%s", apiKey)
	resp, err := postJSON(ctx, url, nil, body)
	if err != nil {
		return "", nil // network error: fall back to Groq, matching the original's tolerance
	}
	if errVal, hasErr := resp["error"]; hasErr {
		// 429/503/500 (quota/rate-limit/transient) fall back silently; anything
		// else is a real error, matching the original's distinction.
		if errMap, ok := errVal.(map[string]any); ok {
			if code, ok := errMap["code"].(float64); ok && (code == 429 || code == 503 || code == 500) {
				return "", nil
			}
		}
		return "", fmt.Errorf("gemini error: %v", errVal)
	}
	text, _ := getString(resp, "candidates", "0", "content", "parts", "0", "text")
	return strings.TrimSpace(text), nil
}

func callGroqForName(ctx context.Context, apiKey, prompt string) (string, error) {
	if apiKey == "" {
		return "", nil
	}
	systemPrompt := "You are a product data specialist for a Kenyan tech retail store. Always respond with valid JSON only — no markdown, no code fences, no explanation."
	body := map[string]any{
		"model":       "llama-3.1-8b-instant",
		"temperature": 0.3,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": prompt},
		},
	}
	headers := map[string]string{"Authorization": "Bearer " + apiKey}
	resp, err := postJSON(ctx, "https://api.groq.com/openai/v1/chat/completions", headers, body)
	if err != nil {
		return "", nil
	}
	if _, hasErr := resp["error"]; hasErr {
		return "", nil
	}
	text, _ := getString(resp, "choices", "0", "message", "content")
	return strings.TrimSpace(text), nil
}

func buildSuggestNamePrompt(name, brand, sku, shortDescription, description, imageURL string, categoryTree map[string][]string) string {
	treeJSON, _ := json.MarshalIndent(categoryTree, "", "  ")
	imageLine := ""
	if imageURL != "" {
		imageLine = fmt.Sprintf("- Image URL: %q\n", imageURL)
	}
	return fmt.Sprintf(`You are a product data specialist for a Kenyan tech retail store.
Your job is to clean up product listings by generating accurate names and selecting the correct Category Hierarchy.

Rules:
- Product name must be concise, professional, and descriptive.
- Do NOT use the word "Generic", filler text, or placeholder words.
- Do NOT include the category name inside the product name.
- You MUST select a "suggestedRootCategory" and "suggestedSubCategory".
- PREFER using the categories from the provided "Existing Category Tree".
- IF AND ONLY IF the product does not fit into any existing Root and Subcategory, you may invent a highly relevant new Root or Subcategory name.
- Prefer specificity: choose the most precise Subcategory that fits.
- Use the SKU, brand, and description as your primary signals for categorization.
- Generate a "suggestedSummary" (1-2 sentences of engaging plain text) if the current short description is empty or poor.
- Generate a "suggestedDescription" (a detailed, professional HTML-formatted description with <p> and <ul> tags) if the current description is empty or poor.

Product Details:
- Current Name: %q
- Brand: %q
- SKU: %q
- Short Description: %q
- Description: %q
%s
Existing Category Tree (JSON format: Root -> [Subcategories]):
%s

Return only a JSON object with keys "suggestedName", "suggestedRootCategory", "suggestedSubCategory", and optionally "suggestedSummary" and "suggestedDescription".`,
		name, brand, sku, shortDescription, description, imageLine, treeJSON)
}

type SuggestNameInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Name             string              `json:"name,omitempty"`
		Description      string              `json:"description,omitempty"`
		ShortDescription string              `json:"short_description,omitempty"`
		Brand            string              `json:"brand,omitempty"`
		SKU              string              `json:"sku,omitempty"`
		ImageURL         string              `json:"image_url,omitempty"`
		CategoryTree     map[string][]string `json:"categoryTree"`
	}
}

type SuggestNameOutput struct {
	Body struct {
		SuggestedName         string `json:"suggestedName"`
		SuggestedRootCategory string `json:"suggestedRootCategory,omitempty"`
		SuggestedSubCategory  string `json:"suggestedSubCategory,omitempty"`
		SuggestedSummary      string `json:"suggestedSummary,omitempty"`
		SuggestedDescription  string `json:"suggestedDescription,omitempty"`
		Source                string `json:"source,omitempty"`
		Warning               string `json:"warning,omitempty"`
	}
}

func RegisterSuggestName(api huma.API, cfg Config, authSvc *auth.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "suggest-name",
		Method:      "POST",
		Path:        "/v1/admin/ai/suggest-name",
		Summary:     "AI-assisted product name/category suggestion for catalog import",
	}, func(ctx context.Context, input *SuggestNameInput) (*SuggestNameOutput, error) {
		if _, err := authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
			return nil, err
		}

		if len(input.Body.CategoryTree) == 0 {
			return nil, huma.Error400BadRequest("No valid category tree provided.")
		}

		prompt := buildSuggestNamePrompt(
			input.Body.Name, input.Body.Brand, input.Body.SKU,
			input.Body.ShortDescription, input.Body.Description, input.Body.ImageURL,
			input.Body.CategoryTree,
		)

		raw, err := callGeminiForName(ctx, cfg.GeminiAPIKey, prompt)
		if err != nil {
			return nil, huma.Error500InternalServerError(err.Error())
		}
		source := "gemini"
		if raw == "" {
			raw, err = callGroqForName(ctx, cfg.GroqAPIKey, prompt)
			if err != nil {
				return nil, huma.Error500InternalServerError(err.Error())
			}
			source = "groq"
		}

		out := &SuggestNameOutput{}
		clean := stripCodeFence(raw)
		var parsed struct {
			SuggestedName         string `json:"suggestedName"`
			SuggestedRootCategory string `json:"suggestedRootCategory"`
			SuggestedSubCategory  string `json:"suggestedSubCategory"`
			SuggestedSummary      string `json:"suggestedSummary"`
			SuggestedDescription  string `json:"suggestedDescription"`
		}
		if clean != "" && json.Unmarshal([]byte(clean), &parsed) == nil &&
			parsed.SuggestedRootCategory != "" && parsed.SuggestedSubCategory != "" {
			out.Body.SuggestedName = parsed.SuggestedName
			if out.Body.SuggestedName == "" {
				out.Body.SuggestedName = input.Body.Name
			}
			out.Body.SuggestedRootCategory = parsed.SuggestedRootCategory
			out.Body.SuggestedSubCategory = parsed.SuggestedSubCategory
			out.Body.SuggestedSummary = parsed.SuggestedSummary
			out.Body.SuggestedDescription = parsed.SuggestedDescription
			out.Body.Source = source
			return out, nil
		}

		out.Body.SuggestedName = input.Body.Name
		out.Body.Warning = "Both Gemini and Groq could not determine a category. Please assign manually."
		return out, nil
	})
}
