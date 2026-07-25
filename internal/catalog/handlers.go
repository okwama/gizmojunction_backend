package catalog

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
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
	Slug     string  `path:"slug"`
	Page     int     `query:"page" default:"1" minimum:"1"`
	PageSize int     `query:"page_size" default:"24" minimum:"1" maximum:"100"`
	Sort     string  `query:"sort" default:"newest" enum:"newest,price_asc,price_desc,best_match"`
	Brands   string  `query:"brands" doc:"comma-separated brand names, multi-select refinement"`
	MinPrice float64 `query:"min_price" minimum:"0"`
	MaxPrice float64 `query:"max_price" minimum:"0"`
	Rating   int     `query:"rating" default:"0" minimum:"0" maximum:"4" doc:"minimum star rating, e.g. 4 = 4 & up"`
	InStock  bool    `query:"in_stock" default:"false"`
}

type CategoryProductsResponse struct {
	Category *Category        `json:"category,omitempty"`
	Products []ProductSummary `json:"products"`
	Facets   Facets           `json:"facets"`
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

func (h *Handlers) GetCategoryProducts(ctx context.Context, input *CategoryProductsInput) (*struct{ Body CategoryProductsResponse }, error) {
	offset := (input.Page - 1) * input.PageSize
	filter := ProductFilter{
		MinPrice:  input.MinPrice,
		MaxPrice:  input.MaxPrice,
		MinRating: input.Rating,
		InStock:   input.InStock,
		Brands:    splitAndTrim(input.Brands),
	}

	if input.Slug == "all" {
		var products []ProductSummary
		var total int
		var facets Facets
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() (err error) {
			products, err = h.repo.ProductsAll(gctx, filter, input.Sort, input.PageSize, offset)
			return
		})
		g.Go(func() (err error) { total, err = h.repo.CountProductsAll(gctx, filter); return })
		g.Go(func() (err error) { facets.Categories, err = h.repo.DepartmentFacets(gctx, filter); return })
		g.Go(func() (err error) { facets.Brands, err = h.repo.BrandFacets(gctx, nil, filter); return })
		g.Go(func() (err error) { facets.Ratings, err = h.repo.RatingFacet(gctx, nil, filter); return })
		if err := g.Wait(); err != nil {
			return nil, huma.Error500InternalServerError("failed to load products", err)
		}
		if products == nil {
			products = []ProductSummary{}
		}
		facets.Normalize()
		return &struct{ Body CategoryProductsResponse }{Body: CategoryProductsResponse{
			Products: products, Facets: facets, Total: total, Page: input.Page, PageSize: input.PageSize,
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

	var products []ProductSummary
	var total int
	var facets Facets
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		products, err = h.repo.ProductsByCategoryIDs(gctx, categoryIDs, filter, input.Sort, input.PageSize, offset)
		return
	})
	g.Go(func() (err error) { total, err = h.repo.CountProductsByCategoryIDs(gctx, categoryIDs, filter); return })
	g.Go(func() (err error) { facets.Brands, err = h.repo.BrandFacets(gctx, categoryIDs, filter); return })
	g.Go(func() (err error) { facets.Ratings, err = h.repo.RatingFacet(gctx, categoryIDs, filter); return })
	if cat.ParentID == nil {
		// Top-level department: offer its direct subcategories to drill into.
		// A subcategory (ParentID != nil) has nothing further under it, per
		// the 2-level assumption — Facets.Categories stays empty.
		g.Go(func() (err error) { facets.Categories, err = h.repo.SubcategoryFacets(gctx, cat.ID, filter); return })
	}
	if err := g.Wait(); err != nil {
		return nil, huma.Error500InternalServerError("failed to load products", err)
	}
	if products == nil {
		products = []ProductSummary{}
	}
	facets.Normalize()

	catCopy := cat
	return &struct{ Body CategoryProductsResponse }{Body: CategoryProductsResponse{
		Category: &catCopy, Products: products, Facets: facets,
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
