package search

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"gizmojunction/backend/internal/auth"
)

// Register wires the public, unauthenticated search endpoint — the
// typo-tolerant replacement for the frontend's previous direct
// supabase.from('products')...ilike(...) calls (header suggestions +
// /search results page).
func Register(api huma.API, client *Client) {
	h := &searchHandlers{client: client}
	huma.Register(api, huma.Operation{
		OperationID: "search-products",
		Method:      http.MethodGet,
		Path:        "/v1/search",
		Summary:     "Typo-tolerant product search",
	}, h.Search)
}

type searchHandlers struct {
	client *Client
}

type SearchInput struct {
	Q        string `query:"q"`
	Limit    int64  `query:"limit" default:"24" minimum:"1" maximum:"100"`
	Category string `query:"category" doc:"category slug, optional"`
}

func (h *searchHandlers) Search(ctx context.Context, input *SearchInput) (*struct{ Body []ProductDoc }, error) {
	if input.Q == "" {
		return &struct{ Body []ProductDoc }{Body: []ProductDoc{}}, nil
	}
	docs, err := h.client.Search(ctx, input.Q, input.Limit, input.Category)
	if err != nil {
		return nil, huma.Error500InternalServerError("search failed", err)
	}
	return &struct{ Body []ProductDoc }{Body: docs}, nil
}

// RegisterAdmin wires the "rebuild the index from scratch" endpoint — run
// once after this feature is first deployed (to backfill existing
// products) and any time the index is suspected to have drifted; ongoing
// freshness otherwise comes from the write-path hooks in
// catalog.AdminHandlers.
func RegisterAdmin(api huma.API, client *Client, authSvc *auth.Service) {
	h := &adminHandlers{client: client, authSvc: authSvc}
	huma.Register(api, huma.Operation{
		OperationID: "admin-reindex-search",
		Method:      http.MethodPost,
		Path:        "/v1/admin/search/reindex",
		Summary:     "Rebuild the product search index from Postgres (admin only)",
	}, h.Reindex)
}

type adminHandlers struct {
	client  *Client
	authSvc *auth.Service
}

type reindexInput struct {
	Authorization string `header:"Authorization"`
}

type reindexOutput struct {
	Indexed int `json:"indexed"`
}

func (h *adminHandlers) Reindex(ctx context.Context, input *reindexInput) (*struct{ Body reindexOutput }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	docs, err := AllProductDocs(ctx, h.client.pool)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load products", err)
	}
	if err := h.client.IndexProducts(ctx, docs); err != nil {
		return nil, huma.Error500InternalServerError("failed to index products", err)
	}
	return &struct{ Body reindexOutput }{Body: reindexOutput{Indexed: len(docs)}}, nil
}
