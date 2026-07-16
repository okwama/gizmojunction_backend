package suppliersync

import (
	"math"
	"time"
)

func BuildDiffReport(
	enriched []EnrichedProduct,
	existing []SupplierProduct,
	storeProducts []StoreProduct,
	sourceVersion string,
	unmappedCategories []UnmappedCategory,
	hasBlockingUnmapped bool,
) DiffReport {

	existingMap := make(map[string]SupplierProduct)
	for _, p := range existing {
		existingMap[p.PartNo] = p
	}

	storeMap := make(map[string]StoreProduct)
	for _, sp := range storeProducts {
		storeMap[sp.PartNo] = sp
	}

	var newProducts []EnrichedProduct
	var priceChanges []PriceChange
	var availabilityChanges []AvailabilityChange
	var discontinued []SupplierProduct

	unchangedCount := 0
	ignoredCount := 0

	incomingPartNos := make(map[string]bool)

	for _, ep := range enriched {
		incomingPartNos[ep.PartNo] = true

		if ep.CategoryState == "ignored" {
			ignoredCount++
			continue
		}

		ex, found := existingMap[ep.PartNo]
		if !found {
			newProducts = append(newProducts, ep)
			continue
		}

		priceChanged := ex.SupplierPrice != ep.SupplierPrice
		availChanged := ex.Availability != ep.Availability

		if !priceChanged && !availChanged {
			unchangedCount++
			continue
		}

		sp, isListed := storeMap[ep.PartNo]
		inStore := isListed && sp.IsListed

		if priceChanged {
			delta := ep.SupplierPrice - ex.SupplierPrice
			var pct float64
			if ex.SupplierPrice > 0 {
				pct = float64(delta) / float64(ex.SupplierPrice) * 100
			}

			pc := PriceChange{
				PartNo:            ep.PartNo,
				Brand:             ep.Brand,
				SupplierCategory:  ep.SupplierCategory,
				StoreCategoryName: ep.StoreCategoryName,
				Description:       ep.Description,
				OldPrice:          ex.SupplierPrice,
				NewPrice:          ep.SupplierPrice,
				PriceDelta:        delta,
				PctChange:         int(math.Round(pct)),
				Availability:      ep.Availability,
				IsStoreListed:     inStore,
			}
			if inStore {
				pc.StoreSKU = sp.StoreSKU
				pc.StorePrice = sp.StorePrice
			}
			priceChanges = append(priceChanges, pc)
		}

		if availChanged {
			ac := AvailabilityChange{
				PartNo:          ep.PartNo,
				Brand:           ep.Brand,
				Description:     ep.Description,
				OldAvailability: ex.Availability,
				NewAvailability: ep.Availability,
				SupplierPrice:   ep.SupplierPrice,
				IsStoreListed:   inStore,
			}
			if inStore {
				ac.StoreSKU = sp.StoreSKU
			}
			availabilityChanges = append(availabilityChanges, ac)
		}
	}

	// Calculate discontinued
	for _, ex := range existing {
		if !incomingPartNos[ex.PartNo] {
			sp, isListed := storeMap[ex.PartNo]
			ex.IsStoreListed = isListed && sp.IsListed
			if ex.IsStoreListed {
				ex.StoreSKU = sp.StoreSKU
			}
			discontinued = append(discontinued, ex)
		}
	}

	summary := DiffSummary{
		TotalIncoming:       len(enriched),
		NewProducts:         len(newProducts),
		PriceChanges:        len(priceChanges),
		AvailabilityChanges: len(availabilityChanges),
		Discontinued:        len(discontinued),
		Unchanged:           unchangedCount,
		Ignored:             ignoredCount,
	}

	// ensure empty slices marshal to [] instead of null
	if newProducts == nil {
		newProducts = []EnrichedProduct{}
	}
	if priceChanges == nil {
		priceChanges = []PriceChange{}
	}
	if availabilityChanges == nil {
		availabilityChanges = []AvailabilityChange{}
	}
	if discontinued == nil {
		discontinued = []SupplierProduct{}
	}
	if unmappedCategories == nil {
		unmappedCategories = []UnmappedCategory{}
	}

	return DiffReport{
		SourceVersion:       sourceVersion,
		GeneratedAt:         time.Now().UTC().Format(time.RFC3339),
		HasBlockingUnmapped: hasBlockingUnmapped,
		UnmappedCategories:  unmappedCategories,
		Summary:             summary,
		NewProducts:         newProducts,
		PriceChanges:        priceChanges,
		AvailabilityChanges: availabilityChanges,
		Discontinued:        discontinued,
		UnchangedCount:      unchangedCount,
	}
}
