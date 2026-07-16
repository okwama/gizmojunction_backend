package suppliersync

import (
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)


func normalizeAvailability(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	switch lower {
	case "ex-stock":
		return "Ex-Stock"
	case "check availability":
		return "Check Availability"
	case "clarance sale", "clearance sale":
		return "Clearance Sale"
	default:
		return "Unknown"
	}
}

func getBrandFromSheet(sheetName string) string {
	switch sheetName {
	case "HP":
		return "HP"
	case "Lenovo":
		return "Lenovo"
	case "Dell":
		return "Dell"
	case "Asus":
		return "Asus"
	case "EVI UPS":
		return "EVI"
	case "Epson":
		return "Epson"
	case "Logitech":
		return "Logitech"
	case "RAPOO":
		return "RAPOO"
	default:
		return "Multi-Brand"
	}
}

func ParseSupplierExcel(file io.Reader, sourceVersion string) ([]SupplierProduct, error) {
	f, err := excelize.OpenReader(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var products []SupplierProduct
	seen := make(map[string]bool)

	for _, sheetName := range f.GetSheetList() {
		lowerSheet := strings.ToLower(sheetName)
		if lowerSheet == "home page" || lowerSheet == "rental" || lowerSheet == "services" {
			continue
		}

		brand := getBrandFromSheet(sheetName)
		rows, err := f.GetRows(sheetName)
		if err != nil {
			continue
		}

		currentCategory := sheetName
		for rowIndex, row := range rows {
			if rowIndex == 0 {
				continue // skip header row
			}

			// pad row to 4 columns to avoid index out of range
			for len(row) < 4 {
				row = append(row, "")
			}

			col0 := strings.TrimSpace(row[0])
			col1 := strings.TrimSpace(row[1])
			col2 := strings.TrimSpace(row[2])
			col3 := strings.TrimSpace(row[3])

			if strings.HasPrefix(col0, "→") || strings.HasPrefix(col1, "→") {
				continue // footer row
			}

			if sheetName == "Logitech" && col0 == "989-000434" {
				continue // anomalous row
			}

			// Category Header Row Detection
			// Strip commas and any non-numeric characters for price parsing
			cleanPrice := strings.Map(func(r rune) rune {
				if (r >= '0' && r <= '9') || r == '-' {
					return r
				}
				if r == '.' {
					return -1 // stop at decimal if any
				}
				return -1
			}, col3)

			// Handle potential decimals by taking only the integer part
			if idx := strings.Index(col3, "."); idx != -1 {
				cleanPrice = strings.Map(func(r rune) rune {
					if (r >= '0' && r <= '9') || r == '-' {
						return r
					}
					return -1
				}, col3[:idx])
			}

			price, err := strconv.Atoi(cleanPrice)
			isPriceMissing := err != nil || cleanPrice == ""

			if col0 == "" && col1 != "" && (isPriceMissing || price <= 0) {
				currentCategory = col1
				continue
			}

			if col0 == "" {
				continue // no part number
			}

			if seen[col0] {
				continue // duplicate part number in file
			}
			seen[col0] = true

			if isPriceMissing || price <= 0 {
				continue // invalid price
			}

			p := SupplierProduct{
				PartNo:           col0,
				Brand:            brand,
				SheetName:        sheetName,
				SupplierCategory: currentCategory,
				Description:      col1,
				Availability:     normalizeAvailability(col2),
				SupplierPrice:    price,
				SourceVersion:    sourceVersion,
			}
			products = append(products, p)
		}
	}

	if len(products) == 0 {
		return nil, errors.New("no products found in file")
	}

	return products, nil
}
