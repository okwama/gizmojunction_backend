// Package search wraps Meilisearch for storefront product search (typeahead
// suggestions + the /search results page), replacing the frontend's direct
// Postgres ILIKE queries against Supabase. It's optional infrastructure: if
// MEILI_HOST isn't configured, cmd/api never registers these routes and
// catalog writes simply skip indexing — the same "disabled until
// configured" pattern already used for R2 storage and KRA eTIMS.
package search

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/meilisearch/meilisearch-go"
)

const productsIndexUID = "products"

// Client wraps the Meilisearch "products" index. It implements
// catalog.ProductIndexer so admin catalog handlers can keep the index in
// sync on every write.
type Client struct {
	index meilisearch.IndexManager
	pool  *pgxpool.Pool
}

func NewClient(host, apiKey string, pool *pgxpool.Pool) *Client {
	sm := meilisearch.New(host, meilisearch.WithAPIKey(apiKey))
	return &Client{index: sm.Index(productsIndexUID), pool: pool}
}

// EnsureIndex configures searchable/filterable/sortable attributes. Safe to
// call on every startup — Meilisearch settings updates are idempotent, and
// the index itself is created automatically on first use if it doesn't
// exist yet, with "id" inferred as the primary key from ProductDoc.
func (c *Client) EnsureIndex(ctx context.Context) error {
	if _, err := c.index.UpdateSearchableAttributesWithContext(ctx, &[]string{"name", "sku", "brand"}); err != nil {
		return err
	}
	filterable := []interface{}{"category_id", "category_slug", "brand", "is_published"}
	if _, err := c.index.UpdateFilterableAttributesWithContext(ctx, &filterable); err != nil {
		return err
	}
	if _, err := c.index.UpdateSortableAttributesWithContext(ctx, &[]string{"price", "rating"}); err != nil {
		return err
	}
	return nil
}

// IndexProduct upserts a single product document (Meilisearch's
// AddDocuments call is an upsert keyed by the primary key).
func (c *Client) IndexProduct(ctx context.Context, doc ProductDoc) error {
	_, err := c.index.AddDocumentsWithContext(ctx, []ProductDoc{doc}, nil)
	return err
}

// IndexProducts bulk-upserts, used by the admin reindex endpoint.
func (c *Client) IndexProducts(ctx context.Context, docs []ProductDoc) error {
	if len(docs) == 0 {
		return nil
	}
	_, err := c.index.AddDocumentsWithContext(ctx, docs, nil)
	return err
}

func (c *Client) DeleteProduct(ctx context.Context, id string) error {
	_, err := c.index.DeleteDocumentWithContext(ctx, id, nil)
	return err
}

// IndexProductByID re-reads the product from Postgres and upserts it —
// the catalog.ProductIndexer method called after every admin product save,
// so the index always reflects fresh DB state (including rating/
// review_count) rather than whatever partial shape the write-path handler
// had in hand.
func (c *Client) IndexProductByID(ctx context.Context, id string) error {
	doc, err := ProductDocByID(ctx, c.pool, id)
	if err != nil {
		return err
	}
	if doc == nil {
		// Row no longer exists (e.g. deleted concurrently) — make sure it's
		// not left stale in the index.
		return c.DeleteProduct(ctx, id)
	}
	return c.IndexProduct(ctx, *doc)
}

// DeleteAllProducts backs the admin "Empty Catalog" action.
func (c *Client) DeleteAllProducts(ctx context.Context) error {
	_, err := c.index.DeleteAllDocumentsWithContext(ctx, nil)
	return err
}

// Search runs a typo-tolerant query, always restricted to published
// products, optionally further restricted to one category (by slug — the
// header's department selector passes this through).
func (c *Client) Search(ctx context.Context, query string, limit int64, categorySlug string) ([]ProductDoc, error) {
	filter := []string{"is_published = true"}
	if categorySlug != "" {
		filter = append(filter, `category_slug = "`+strings.ReplaceAll(categorySlug, `"`, `\"`)+`"`)
	}
	resp, err := c.index.SearchWithContext(ctx, query, &meilisearch.SearchRequest{
		Limit:  limit,
		Filter: filter,
	})
	if err != nil {
		return nil, err
	}
	docs := make([]ProductDoc, 0, len(resp.Hits))
	for _, hit := range resp.Hits {
		var doc ProductDoc
		if err := hit.DecodeInto(&doc); err != nil {
			continue
		}
		docs = append(docs, doc)
	}
	return docs, nil
}
