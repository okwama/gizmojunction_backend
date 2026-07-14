package catalog

import "context"

type HomeResponse struct {
	FeaturedProducts   []ProductSummary            `json:"featured_products"`
	NewArrivals        []ProductSummary            `json:"new_arrivals"`
	FeaturedCategories []CategoryWithSubcategories `json:"featured_categories"`
	MidPagePromos      []Promotion                 `json:"mid_page_promos"`
	HeroPromos         []Promotion                 `json:"hero_promos"`
	RecentBlogPosts    []BlogPostSummary           `json:"recent_blog_posts"`
}

const maxSubcategoriesPerCategory = 4

// GetHome replaces the original page load's up-to-~52 sequential Supabase
// round trips (1 per featured category, then 1 per subcategory, then up to
// 3 fallback image lookups per subcategory) with a fixed, small number of
// set-based queries — the query count no longer grows with the number of
// categories or subcategories.
func (r *Repo) GetHome(ctx context.Context) (HomeResponse, error) {
	featured, err := r.FeaturedProducts(ctx, 8)
	if err != nil {
		return HomeResponse{}, err
	}
	if len(featured) == 0 {
		featured, err = r.RecentProductsAnyStatus(ctx, 8)
		if err != nil {
			return HomeResponse{}, err
		}
	}

	newArrivals, err := r.NewArrivals(ctx, 12)
	if err != nil {
		return HomeResponse{}, err
	}

	featuredCats, err := r.FeaturedCategories(ctx)
	if err != nil {
		return HomeResponse{}, err
	}

	parentIDs := make([]string, len(featuredCats))
	for i, c := range featuredCats {
		parentIDs[i] = c.ID
	}

	allSubcats, err := r.SubcategoriesByParentIDs(ctx, parentIDs)
	if err != nil {
		return HomeResponse{}, err
	}

	subcatsByParent := map[string][]Category{}
	for _, sub := range allSubcats {
		if sub.ParentID == nil {
			continue
		}
		if len(subcatsByParent[*sub.ParentID]) >= maxSubcategoriesPerCategory {
			continue
		}
		subcatsByParent[*sub.ParentID] = append(subcatsByParent[*sub.ParentID], sub)
	}

	var keptSubcatIDs []string
	for _, subs := range subcatsByParent {
		for _, s := range subs {
			keptSubcatIDs = append(keptSubcatIDs, s.ID)
		}
	}

	// Tier 1: image sourced from the subcategory itself.
	imagesBySubcat, err := r.RepresentativeImagesByCategoryIDs(ctx, keptSubcatIDs)
	if err != nil {
		return HomeResponse{}, err
	}

	// Tier 2: for subcategories with no product image, fall back to their
	// parent category's representative image.
	var missingParents []string
	for parentID, subs := range subcatsByParent {
		for _, s := range subs {
			if _, ok := imagesBySubcat[s.ID]; !ok {
				missingParents = append(missingParents, parentID)
				break
			}
		}
	}
	imagesByParent, err := r.RepresentativeImagesByCategoryIDs(ctx, dedupe(missingParents))
	if err != nil {
		return HomeResponse{}, err
	}

	// Tier 3: last resort — any product image at all. Fetched lazily, at
	// most once, only if something still has no image after tiers 1 and 2.
	var globalFallback string
	globalFallbackLoaded := false
	loadGlobalFallback := func() (string, error) {
		if !globalFallbackLoaded {
			img, err := r.AnyProductImage(ctx)
			if err != nil {
				return "", err
			}
			globalFallback = img
			globalFallbackLoaded = true
		}
		return globalFallback, nil
	}

	result := make([]CategoryWithSubcategories, 0, len(featuredCats))
	for _, cat := range featuredCats {
		subs := subcatsByParent[cat.ID]
		enriched := make([]SubcategoryWithImage, 0, len(subs))
		for _, s := range subs {
			img := imagesBySubcat[s.ID]
			if img == "" {
				img = imagesByParent[cat.ID]
			}
			if img == "" {
				img, err = loadGlobalFallback()
				if err != nil {
					return HomeResponse{}, err
				}
			}
			var imgPtr *string
			if img != "" {
				imgPtr = &img
			}
			enriched = append(enriched, SubcategoryWithImage{Category: s, FallbackImage: imgPtr})
		}
		result = append(result, CategoryWithSubcategories{Category: cat, Subcategories: enriched})
	}

	midPromos, err := r.ActivePromotionsByLocation(ctx, "homepage-mid", 2)
	if err != nil {
		return HomeResponse{}, err
	}

	heroPromos, err := r.ActivePromotionsByLocation(ctx, "hero", 50)
	if err != nil {
		return HomeResponse{}, err
	}

	blogs, err := r.RecentBlogPosts(ctx, 4)
	if err != nil {
		return HomeResponse{}, err
	}

	return HomeResponse{
		FeaturedProducts:   featured,
		NewArrivals:        newArrivals,
		FeaturedCategories: result,
		MidPagePromos:      midPromos,
		HeroPromos:         heroPromos,
		RecentBlogPosts:    blogs,
	}, nil
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
