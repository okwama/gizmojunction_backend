package suppliersync

func ResolveCategories(products []SupplierProduct, mappings []CategoryMapping) CategoryResolutionResult {
	mappingDict := make(map[string]CategoryMapping)
	for _, m := range mappings {
		mappingDict[m.SupplierCategory] = m
	}

	var enriched []EnrichedProduct
	unmappedMap := make(map[string]*UnmappedCategory)

	for _, p := range products {
		ep := EnrichedProduct{
			SupplierProduct: p,
		}

		m, found := mappingDict[p.SupplierCategory]
		if found {
			if m.IsIgnored {
				ep.CategoryState = "ignored"
			} else if m.StoreCategoryID != nil {
				ep.CategoryState = "mapped"
				ep.StoreCategoryID = m.StoreCategoryID
				if m.StoreCategoryName != nil {
					ep.StoreCategoryName = *m.StoreCategoryName
				}
			} else {
				ep.CategoryState = "unmapped"
			}
		} else {
			ep.CategoryState = "unmapped"
		}

		if ep.CategoryState == "unmapped" {
			key := p.SheetName + "::" + p.SupplierCategory
			if _, exists := unmappedMap[key]; !exists {
				unmappedMap[key] = &UnmappedCategory{
					SupplierSheet:    p.SheetName,
					SupplierCategory: p.SupplierCategory,
					ProductCount:     0,
					SampleProducts:   []SupplierProduct{},
				}
			}
			uc := unmappedMap[key]
			uc.ProductCount++
			if len(uc.SampleProducts) < 3 {
				uc.SampleProducts = append(uc.SampleProducts, p)
			}
		}

		enriched = append(enriched, ep)
	}

	var unmappedList []UnmappedCategory
	for _, uc := range unmappedMap {
		unmappedList = append(unmappedList, *uc)
	}

	return CategoryResolutionResult{
		EnrichedProducts:    enriched,
		UnmappedCategories:  unmappedList,
		HasBlockingUnmapped: len(unmappedList) > 0,
	}
}
