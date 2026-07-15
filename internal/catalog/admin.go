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
