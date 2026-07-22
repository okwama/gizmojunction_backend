package catalog

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"gizmojunction/backend/internal/auth"
)

// AdminHandlers holds the write-capable admin endpoints for
// categories/brands/products — everything here is gated by
// RequireRole(ADMIN), unlike the read-only, unauthenticated Handlers in
// handlers.go.
type AdminHandlers struct {
	repo    *Repo
	authSvc *auth.Service
}

// RegisterAdmin wires the admin catalog management endpoints (Phase 5a) —
// the Go+Neon replacement for the admin categories/brands/products pages'
// previous direct supabase.from(...) calls.
func RegisterAdmin(api huma.API, repo *Repo, authSvc *auth.Service) {
	h := &AdminHandlers{repo: repo, authSvc: authSvc}

	huma.Register(api, huma.Operation{
		OperationID: "admin-list-categories",
		Method:      http.MethodGet,
		Path:        "/v1/admin/categories",
		Summary:     "List all categories (admin only)",
	}, h.ListCategories)

	huma.Register(api, huma.Operation{
		OperationID: "admin-save-category",
		Method:      http.MethodPost,
		Path:        "/v1/admin/categories",
		Summary:     "Create or update a category (admin only)",
	}, h.SaveCategory)

	huma.Register(api, huma.Operation{
		OperationID: "admin-delete-category",
		Method:      http.MethodDelete,
		Path:        "/v1/admin/categories/{id}",
		Summary:     "Delete a category (admin only)",
	}, h.DeleteCategory)

	huma.Register(api, huma.Operation{
		OperationID: "admin-merge-categories",
		Method:      http.MethodPost,
		Path:        "/v1/admin/categories/merge",
		Summary:     "Merge one category into another and delete the source (admin only)",
	}, h.MergeCategories)

	huma.Register(api, huma.Operation{
		OperationID: "admin-list-brands",
		Method:      http.MethodGet,
		Path:        "/v1/admin/brands",
		Summary:     "List all brands (admin only)",
	}, h.ListBrands)

	huma.Register(api, huma.Operation{
		OperationID: "admin-save-brand",
		Method:      http.MethodPost,
		Path:        "/v1/admin/brands",
		Summary:     "Create or update a brand (admin only)",
	}, h.SaveBrand)

	huma.Register(api, huma.Operation{
		OperationID: "admin-delete-brand",
		Method:      http.MethodDelete,
		Path:        "/v1/admin/brands/{id}",
		Summary:     "Delete a brand (admin only)",
	}, h.DeleteBrand)

	huma.Register(api, huma.Operation{
		OperationID: "admin-list-products",
		Method:      http.MethodGet,
		Path:        "/v1/admin/products",
		Summary:     "Paginated/searchable product list (admin only)",
	}, h.ListProducts)

	huma.Register(api, huma.Operation{
		OperationID: "admin-save-product",
		Method:      http.MethodPost,
		Path:        "/v1/admin/products",
		Summary:     "Create or update a product (admin only)",
	}, h.SaveProduct)

	huma.Register(api, huma.Operation{
		OperationID: "admin-delete-product",
		Method:      http.MethodDelete,
		Path:        "/v1/admin/products/{id}",
		Summary:     "Delete a product (admin only)",
	}, h.DeleteProduct)

	huma.Register(api, huma.Operation{
		OperationID: "admin-bulk-update-product-category",
		Method:      http.MethodPost,
		Path:        "/v1/admin/products/bulk-category",
		Summary:     "Reassign category for a set of products (admin only)",
	}, h.BulkUpdateCategory)

	huma.Register(api, huma.Operation{
		OperationID: "admin-bulk-update-product-status",
		Method:      http.MethodPost,
		Path:        "/v1/admin/products/bulk-status",
		Summary:     "Publish/unpublish a set of products (admin only)",
	}, h.BulkUpdateStatus)

	huma.Register(api, huma.Operation{
		OperationID: "admin-bulk-adjust-product-price",
		Method:      http.MethodPost,
		Path:        "/v1/admin/products/bulk-price",
		Summary:     "Adjust price by percentage or fixed amount for a set of products (admin only)",
	}, h.BulkAdjustPrice)

	huma.Register(api, huma.Operation{
		OperationID: "admin-bulk-delete-products",
		Method:      http.MethodPost,
		Path:        "/v1/admin/products/bulk-delete",
		Summary:     "Delete a set of products (admin only)",
	}, h.BulkDeleteProducts)

	huma.Register(api, huma.Operation{
		OperationID: "admin-empty-product-catalog",
		Method:      http.MethodPost,
		Path:        "/v1/admin/products/empty-catalog",
		Summary:     "Delete every product (admin only, destructive)",
	}, h.EmptyCatalog)

	huma.Register(api, huma.Operation{
		OperationID: "admin-patch-descriptions",
		Method:      http.MethodPost,
		Path:        "/v1/admin/products/patch-descriptions",
		Summary:     "Fill blank product descriptions by SKU in bulk (admin only)",
	}, h.PatchDescriptions)

	huma.Register(api, huma.Operation{
		OperationID: "admin-list-cleanup-products",
		Method:      http.MethodGet,
		Path:        "/v1/admin/cleanup/products",
		Summary:     "Products needing cleanup, filtered by issue type (admin only)",
	}, h.ListCleanupProducts)

	huma.Register(api, huma.Operation{
		OperationID: "admin-apply-cleanup",
		Method:      http.MethodPost,
		Path:        "/v1/admin/cleanup/apply",
		Summary:     "Apply cleanup updates (names/SKUs/descriptions/categories) in bulk (admin only)",
	}, h.ApplyCleanup)

	huma.Register(api, huma.Operation{
		OperationID: "admin-list-promotions",
		Method:      http.MethodGet,
		Path:        "/v1/admin/promotions",
		Summary:     "List all promotions (admin only)",
	}, h.ListPromotions)

	huma.Register(api, huma.Operation{
		OperationID: "admin-save-promotion",
		Method:      http.MethodPost,
		Path:        "/v1/admin/promotions",
		Summary:     "Create or update a promotion (admin only)",
	}, h.SavePromotion)

	huma.Register(api, huma.Operation{
		OperationID: "admin-delete-promotion",
		Method:      http.MethodDelete,
		Path:        "/v1/admin/promotions/{id}",
		Summary:     "Delete a promotion (admin only)",
	}, h.DeletePromotion)

	huma.Register(api, huma.Operation{
		OperationID: "admin-list-blog-posts",
		Method:      http.MethodGet,
		Path:        "/v1/admin/blogs",
		Summary:     "List all blog posts (admin only)",
	}, h.ListBlogPosts)

	huma.Register(api, huma.Operation{
		OperationID: "admin-save-blog-post",
		Method:      http.MethodPost,
		Path:        "/v1/admin/blogs",
		Summary:     "Create or update a blog post (admin only)",
	}, h.SaveBlogPost)

	huma.Register(api, huma.Operation{
		OperationID: "admin-delete-blog-post",
		Method:      http.MethodDelete,
		Path:        "/v1/admin/blogs/{id}",
		Summary:     "Delete a blog post (admin only)",
	}, h.DeleteBlogPost)
}

type adminAuthInput struct {
	Authorization string `header:"Authorization"`
}

// --- Categories ---

func (h *AdminHandlers) ListCategories(ctx context.Context, input *adminAuthInput) (*struct{ Body []Category }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	cats, err := h.repo.ListCategories(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load categories", err)
	}
	return &struct{ Body []Category }{Body: cats}, nil
}

type SaveCategoryInput struct {
	Authorization string `header:"Authorization"`
	Body          Category
}

func (h *AdminHandlers) SaveCategory(ctx context.Context, input *SaveCategoryInput) (*struct{ Body Category }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.Name == "" || input.Body.Slug == "" {
		return nil, huma.Error400BadRequest("name and slug are required")
	}
	cat, err := h.repo.UpsertCategory(ctx, input.Body)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to save category", err)
	}
	return &struct{ Body Category }{Body: cat}, nil
}

type DeleteCategoryInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

func (h *AdminHandlers) DeleteCategory(ctx context.Context, input *DeleteCategoryInput) (*struct{ Body struct{ Success bool `json:"success"` } }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if err := h.repo.DeleteCategory(ctx, input.ID); err != nil {
		return nil, huma.Error500InternalServerError("failed to delete category", err)
	}
	out := &struct{ Body struct{ Success bool `json:"success"` } }{}
	out.Body.Success = true
	return out, nil
}

type MergeCategoriesInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		SourceID string `json:"source_id"`
		TargetID string `json:"target_id"`
	}
}

func (h *AdminHandlers) MergeCategories(ctx context.Context, input *MergeCategoriesInput) (*struct{ Body struct{ Success bool `json:"success"` } }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.SourceID == "" || input.Body.TargetID == "" || input.Body.SourceID == input.Body.TargetID {
		return nil, huma.Error400BadRequest("source_id and target_id are required and must differ")
	}
	if err := h.repo.MergeCategories(ctx, input.Body.SourceID, input.Body.TargetID); err != nil {
		return nil, huma.Error500InternalServerError("failed to merge categories", err)
	}
	out := &struct{ Body struct{ Success bool `json:"success"` } }{}
	out.Body.Success = true
	return out, nil
}

// --- Brands ---

func (h *AdminHandlers) ListBrands(ctx context.Context, input *adminAuthInput) (*struct{ Body []Brand }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	rows, err := h.repo.pool.Query(ctx, `SELECT `+brandColumns+` FROM brands ORDER BY name ASC`)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load brands", err)
	}
	brands, err := pgx.CollectRows(rows, pgx.RowToStructByName[Brand])
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load brands", err)
	}
	return &struct{ Body []Brand }{Body: brands}, nil
}

type SaveBrandInput struct {
	Authorization string `header:"Authorization"`
	Body          Brand
}

func (h *AdminHandlers) SaveBrand(ctx context.Context, input *SaveBrandInput) (*struct{ Body Brand }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.Name == "" {
		return nil, huma.Error400BadRequest("name is required")
	}
	brand, err := h.repo.UpsertBrand(ctx, input.Body)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to save brand", err)
	}
	return &struct{ Body Brand }{Body: brand}, nil
}

type DeleteBrandInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

func (h *AdminHandlers) DeleteBrand(ctx context.Context, input *DeleteBrandInput) (*struct{ Body struct{ Success bool `json:"success"` } }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if err := h.repo.DeleteBrand(ctx, input.ID); err != nil {
		return nil, huma.Error500InternalServerError("failed to delete brand", err)
	}
	out := &struct{ Body struct{ Success bool `json:"success"` } }{}
	out.Body.Success = true
	return out, nil
}

// --- Products ---

type ListProductsInput struct {
	Authorization string `header:"Authorization"`
	Search        string `query:"search"`
	CategoryID    string `query:"category_id"`
	Page          int    `query:"page" default:"1" minimum:"1"`
	PageSize      int    `query:"page_size" default:"20" minimum:"1" maximum:"100"`
}

type ListProductsResponse struct {
	Products []AdminProduct `json:"products"`
	Total    int            `json:"total"`
}

func (h *AdminHandlers) ListProducts(ctx context.Context, input *ListProductsInput) (*struct{ Body ListProductsResponse }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	offset := (input.Page - 1) * input.PageSize
	products, total, err := h.repo.ListProductsAdmin(ctx, input.Search, input.CategoryID, input.PageSize, offset)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load products", err)
	}
	return &struct{ Body ListProductsResponse }{Body: ListProductsResponse{Products: products, Total: total}}, nil
}

type SaveProductInput struct {
	Authorization string `header:"Authorization"`
	Body          AdminProduct
}

func (h *AdminHandlers) SaveProduct(ctx context.Context, input *SaveProductInput) (*struct{ Body AdminProduct }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.Name == "" || input.Body.SKU == "" {
		return nil, huma.Error400BadRequest("name and sku are required")
	}
	product, err := h.repo.UpsertProduct(ctx, input.Body)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to save product", err)
	}
	return &struct{ Body AdminProduct }{Body: product}, nil
}

type DeleteProductInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

func (h *AdminHandlers) DeleteProduct(ctx context.Context, input *DeleteProductInput) (*struct{ Body struct{ Success bool `json:"success"` } }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if err := h.repo.DeleteProduct(ctx, input.ID); err != nil {
		return nil, huma.Error500InternalServerError("failed to delete product", err)
	}
	out := &struct{ Body struct{ Success bool `json:"success"` } }{}
	out.Body.Success = true
	return out, nil
}

type BulkUpdateCategoryInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		ProductIDs []string `json:"product_ids"`
		CategoryID string   `json:"category_id"`
	}
}

func (h *AdminHandlers) BulkUpdateCategory(ctx context.Context, input *BulkUpdateCategoryInput) (*struct{ Body struct{ Success bool `json:"success"` } }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if len(input.Body.ProductIDs) == 0 {
		return nil, huma.Error400BadRequest("product_ids is required")
	}
	if err := h.repo.BulkUpdateProductCategory(ctx, input.Body.ProductIDs, input.Body.CategoryID); err != nil {
		return nil, huma.Error500InternalServerError("failed to update products", err)
	}
	out := &struct{ Body struct{ Success bool `json:"success"` } }{}
	out.Body.Success = true
	return out, nil
}

type BulkUpdateStatusInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		ProductIDs  []string `json:"product_ids"`
		IsPublished bool     `json:"is_published"`
	}
}

func (h *AdminHandlers) BulkUpdateStatus(ctx context.Context, input *BulkUpdateStatusInput) (*struct{ Body struct{ Success bool `json:"success"` } }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if len(input.Body.ProductIDs) == 0 {
		return nil, huma.Error400BadRequest("product_ids is required")
	}
	if err := h.repo.BulkUpdateProductStatus(ctx, input.Body.ProductIDs, input.Body.IsPublished); err != nil {
		return nil, huma.Error500InternalServerError("failed to update products", err)
	}
	out := &struct{ Body struct{ Success bool `json:"success"` } }{}
	out.Body.Success = true
	return out, nil
}

type BulkAdjustPriceInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		ProductIDs []string `json:"product_ids"`
		// Mode is "percent" (Value is a percentage, e.g. 10 for +10%, -15
		// for -15%) or "fixed" (Value is a KES amount added to the current
		// price, negative to reduce).
		Mode  string  `json:"mode" enum:"percent,fixed"`
		Value float64 `json:"value"`
	}
}

func (h *AdminHandlers) BulkAdjustPrice(ctx context.Context, input *BulkAdjustPriceInput) (*struct{ Body struct{ Success bool `json:"success"` } }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if len(input.Body.ProductIDs) == 0 {
		return nil, huma.Error400BadRequest("product_ids is required")
	}
	if input.Body.Mode != "percent" && input.Body.Mode != "fixed" {
		return nil, huma.Error400BadRequest("mode must be \"percent\" or \"fixed\"")
	}
	if err := h.repo.BulkAdjustPrice(ctx, input.Body.ProductIDs, input.Body.Mode, input.Body.Value); err != nil {
		return nil, huma.Error500InternalServerError("failed to adjust prices", err)
	}
	out := &struct{ Body struct{ Success bool `json:"success"` } }{}
	out.Body.Success = true
	return out, nil
}

type BulkDeleteProductsInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		ProductIDs []string `json:"product_ids"`
	}
}

func (h *AdminHandlers) BulkDeleteProducts(ctx context.Context, input *BulkDeleteProductsInput) (*struct{ Body struct{ Success bool `json:"success"` } }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if len(input.Body.ProductIDs) == 0 {
		return nil, huma.Error400BadRequest("product_ids is required")
	}
	if err := h.repo.BulkDeleteProducts(ctx, input.Body.ProductIDs); err != nil {
		return nil, huma.Error500InternalServerError("failed to delete products", err)
	}
	out := &struct{ Body struct{ Success bool `json:"success"` } }{}
	out.Body.Success = true
	return out, nil
}

func (h *AdminHandlers) EmptyCatalog(ctx context.Context, input *adminAuthInput) (*struct{ Body struct{ Success bool `json:"success"` } }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if err := h.repo.EmptyProductCatalog(ctx); err != nil {
		return nil, huma.Error500InternalServerError("failed to empty catalog", err)
	}
	out := &struct{ Body struct{ Success bool `json:"success"` } }{}
	out.Body.Success = true
	return out, nil
}

// --- Promotions ---

func (h *AdminHandlers) ListPromotions(ctx context.Context, input *adminAuthInput) (*struct{ Body []AdminPromotion }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	promos, err := h.repo.ListPromotionsAdmin(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load promotions", err)
	}
	return &struct{ Body []AdminPromotion }{Body: promos}, nil
}

type SavePromotionInput struct {
	Authorization string `header:"Authorization"`
	Body          AdminPromotion
}

func (h *AdminHandlers) SavePromotion(ctx context.Context, input *SavePromotionInput) (*struct{ Body AdminPromotion }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.Title == "" {
		return nil, huma.Error400BadRequest("title is required")
	}
	if input.Body.DisplayLocation == "" {
		input.Body.DisplayLocation = "hero"
	}
	promo, err := h.repo.UpsertPromotion(ctx, input.Body)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to save promotion", err)
	}
	return &struct{ Body AdminPromotion }{Body: promo}, nil
}

type DeletePromotionInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

func (h *AdminHandlers) DeletePromotion(ctx context.Context, input *DeletePromotionInput) (*struct{ Body struct{ Success bool `json:"success"` } }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if err := h.repo.DeletePromotion(ctx, input.ID); err != nil {
		return nil, huma.Error500InternalServerError("failed to delete promotion", err)
	}
	out := &struct{ Body struct{ Success bool `json:"success"` } }{}
	out.Body.Success = true
	return out, nil
}

// --- Blog posts ---

func (h *AdminHandlers) ListBlogPosts(ctx context.Context, input *adminAuthInput) (*struct{ Body []AdminBlogPost }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	posts, err := h.repo.ListBlogPostsAdmin(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load blog posts", err)
	}
	return &struct{ Body []AdminBlogPost }{Body: posts}, nil
}

type SaveBlogPostInput struct {
	Authorization string `header:"Authorization"`
	Body          AdminBlogPost
}

func (h *AdminHandlers) SaveBlogPost(ctx context.Context, input *SaveBlogPostInput) (*struct{ Body AdminBlogPost }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.Title == "" || input.Body.Slug == "" {
		return nil, huma.Error400BadRequest("title and slug are required")
	}
	post, err := h.repo.UpsertBlogPost(ctx, input.Body)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to save blog post", err)
	}
	return &struct{ Body AdminBlogPost }{Body: post}, nil
}

type DeleteBlogPostInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

func (h *AdminHandlers) DeleteBlogPost(ctx context.Context, input *DeleteBlogPostInput) (*struct{ Body struct{ Success bool `json:"success"` } }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if err := h.repo.DeleteBlogPost(ctx, input.ID); err != nil {
		return nil, huma.Error500InternalServerError("failed to delete blog post", err)
	}
	out := &struct{ Body struct{ Success bool `json:"success"` } }{}
	out.Body.Success = true
	return out, nil
}
