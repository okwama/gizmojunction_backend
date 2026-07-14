package catalog

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
)

type emptyInput struct{}

type Handlers struct {
	repo *Repo
}

// Register wires the five Phase 1 public catalog endpoints — all GET, all
// unauthenticated, no writes — onto the given huma API.
func Register(api huma.API, repo *Repo) {
	h := &Handlers{repo: repo}

	huma.Register(api, huma.Operation{
		OperationID: "get-home",
		Method:      http.MethodGet,
		Path:        "/v1/home",
		Summary:     "Homepage catalog data",
	}, h.GetHome)

	huma.Register(api, huma.Operation{
		OperationID: "list-categories",
		Method:      http.MethodGet,
		Path:        "/v1/categories",
		Summary:     "Flat category list",
	}, h.ListCategories)

	huma.Register(api, huma.Operation{
		OperationID: "get-category-products",
		Method:      http.MethodGet,
		Path:        "/v1/categories/{slug}/products",
		Summary:     "Paginated products within a category (slug \"all\" for the unfiltered listing)",
	}, h.GetCategoryProducts)

	huma.Register(api, huma.Operation{
		OperationID: "get-product",
		Method:      http.MethodGet,
		Path:        "/v1/products/{sku}",
		Summary:     "Product detail by SKU",
	}, h.GetProduct)

	huma.Register(api, huma.Operation{
		OperationID: "list-products-by-ids",
		Method:      http.MethodGet,
		Path:        "/v1/products",
		Summary:     "Batch product lookup by id (recently-viewed)",
	}, h.ListProductsByIDs)
}

func (h *Handlers) GetHome(ctx context.Context, _ *emptyInput) (*struct{ Body HomeResponse }, error) {
	home, err := h.repo.GetHome(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load home data", err)
	}
	return &struct{ Body HomeResponse }{Body: home}, nil
}

func (h *Handlers) ListCategories(ctx context.Context, _ *emptyInput) (*struct{ Body []Category }, error) {
	cats, err := h.repo.ListCategories(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load categories", err)
	}
	return &struct{ Body []Category }{Body: cats}, nil
}

type CategoryProductsInput struct {
	Slug     string `path:"slug"`
	Page     int    `query:"page" default:"1" minimum:"1"`
	PageSize int    `query:"page_size" default:"24" minimum:"1" maximum:"100"`
}

type CategoryProductsResponse struct {
	Category      *Category        `json:"category,omitempty"`
	Products      []ProductSummary `json:"products"`
	AllCategories []Category       `json:"all_categories"`
	Brands        []string         `json:"brands"`
	Total         int              `json:"total"`
	Page          int              `json:"page"`
	PageSize      int              `json:"page_size"`
}

func (h *Handlers) GetCategoryProducts(ctx context.Context, input *CategoryProductsInput) (*struct{ Body CategoryProductsResponse }, error) {
	offset := (input.Page - 1) * input.PageSize

	allCategories, err := h.repo.ListCategories(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load categories", err)
	}

	if input.Slug == "all" {
		products, err := h.repo.ProductsAll(ctx, input.PageSize, offset)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load products", err)
		}
		total, err := h.repo.CountProductsAll(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to count products", err)
		}
		brands, err := h.repo.DistinctBrandsAll(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load brands", err)
		}
		return &struct{ Body CategoryProductsResponse }{Body: CategoryProductsResponse{
			Products: products, AllCategories: allCategories, Brands: brands,
			Total: total, Page: input.Page, PageSize: input.PageSize,
		}}, nil
	}

	cat, err := h.repo.CategoryBySlug(ctx, input.Slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("category not found")
		}
		return nil, huma.Error500InternalServerError("failed to load category", err)
	}

	categoryIDs, err := h.repo.CategoryDescendantIDs(ctx, cat.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to resolve category tree", err)
	}

	products, err := h.repo.ProductsByCategoryIDs(ctx, categoryIDs, input.PageSize, offset)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load products", err)
	}
	total, err := h.repo.CountProductsByCategoryIDs(ctx, categoryIDs)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to count products", err)
	}
	brands, err := h.repo.DistinctBrandsByCategoryIDs(ctx, categoryIDs)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load brands", err)
	}

	catCopy := cat
	return &struct{ Body CategoryProductsResponse }{Body: CategoryProductsResponse{
		Category: &catCopy, Products: products, AllCategories: allCategories, Brands: brands,
		Total: total, Page: input.Page, PageSize: input.PageSize,
	}}, nil
}

type ProductInput struct {
	SKU string `path:"sku"`
}

type ProductResponse struct {
	Product           ProductDetail    `json:"product"`
	Category          *Category        `json:"category,omitempty"`
	Brand             *Brand           `json:"brand,omitempty"`
	SiblingCategories []Category       `json:"sibling_categories"`
	RelatedProducts   []ProductSummary `json:"related_products"`
	UpsellProducts    []ProductSummary `json:"upsell_products"`
	Reviews           []Review         `json:"reviews"`
}

func (h *Handlers) GetProduct(ctx context.Context, input *ProductInput) (*struct{ Body ProductResponse }, error) {
	product, err := h.repo.ProductBySKU(ctx, input.SKU)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("product not found")
		}
		return nil, huma.Error500InternalServerError("failed to load product", err)
	}

	resp := ProductResponse{Product: product}

	var relatedCategoryIDs []string
	if product.CategoryID != nil {
		if cat, err := h.repo.CategoryByID(ctx, *product.CategoryID); err == nil {
			catCopy := cat
			resp.Category = &catCopy
			if cat.ParentID != nil {
				if siblings, err := h.repo.SiblingCategories(ctx, *cat.ParentID); err == nil {
					resp.SiblingCategories = siblings
					for _, s := range siblings {
						relatedCategoryIDs = append(relatedCategoryIDs, s.ID)
					}
				}
			}
		}
		if len(relatedCategoryIDs) == 0 {
			relatedCategoryIDs = []string{*product.CategoryID}
		}
	}

	if product.BrandID != nil {
		if brand, err := h.repo.BrandByID(ctx, *product.BrandID); err == nil {
			brandCopy := brand
			resp.Brand = &brandCopy
		}
	}

	if len(relatedCategoryIDs) > 0 {
		if related, err := h.repo.RelatedProducts(ctx, relatedCategoryIDs, product.ID, 4); err == nil {
			resp.RelatedProducts = related
		}
	}

	if product.Brand != nil {
		if upsell, err := h.repo.UpsellProducts(ctx, *product.Brand, product.ID, 4); err == nil {
			resp.UpsellProducts = upsell
		}
	}

	if reviews, err := h.repo.ReviewsByProductID(ctx, product.ID); err == nil {
		resp.Reviews = reviews
	}

	return &struct{ Body ProductResponse }{Body: resp}, nil
}

type ProductsByIDsInput struct {
	IDs string `query:"ids" doc:"comma-separated product ids"`
}

func (h *Handlers) ListProductsByIDs(ctx context.Context, input *ProductsByIDsInput) (*struct{ Body []ProductSummary }, error) {
	ids := splitAndTrim(input.IDs)
	if len(ids) == 0 {
		return &struct{ Body []ProductSummary }{Body: []ProductSummary{}}, nil
	}
	products, err := h.repo.ProductsByIDs(ctx, ids)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load products", err)
	}
	return &struct{ Body []ProductSummary }{Body: products}, nil
}

func splitAndTrim(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
