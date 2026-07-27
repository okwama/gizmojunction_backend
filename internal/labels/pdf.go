package labels

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/go-pdf/fpdf"
	"github.com/go-pdf/fpdf/contrib/barcode"
)

// LabelLine is one product + how many copies of its label to print — a
// GRN receipt prints one per unit just received; a price-change relabel
// job usually just wants one per product.
type LabelLine struct {
	Name   string
	Price  float64
	Code   string // barcode if the product has one, else its SKU
	Copies int32
}

const (
	pageMargin   = 8.0
	cols         = 3
	rows         = 8
	labelWidth   = 63.0
	labelHeight  = 33.0
	labelPadding = 3.0
)

// renderLabels lays out a grid of shelf labels (name, price, barcode) on
// A4 pages — 3 columns x 8 rows, sized close to a common address-label
// sheet so it can go straight into a printer without custom stock.
func renderLabels(lines []LabelLine) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetAutoPageBreak(false, 0)

	slot := rows * cols
	pos := 0

	for _, line := range lines {
		copies := line.Copies
		if copies < 1 {
			copies = 1
		}
		for i := int32(0); i < copies; i++ {
			if pos%slot == 0 {
				pdf.AddPage()
			}
			col := pos % cols
			row := (pos / cols) % rows
			x := pageMargin + float64(col)*labelWidth
			y := pageMargin + float64(row)*labelHeight
			drawLabel(pdf, x, y, line)
			pos++
		}
	}

	if pos == 0 {
		pdf.AddPage()
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func drawLabel(pdf *fpdf.Fpdf, x, y float64, line LabelLine) {
	pdf.Rect(x, y, labelWidth, labelHeight, "D")

	innerX := x + labelPadding
	innerW := labelWidth - 2*labelPadding

	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetTextColor(20, 20, 20)
	name := truncate(line.Name, 34)
	pdf.SetXY(innerX, y+2)
	pdf.CellFormat(innerW, 5, name, "", 0, "L", false, 0, "")

	pdf.SetFont("Helvetica", "B", 12)
	pdf.SetXY(innerX, y+7.5)
	pdf.CellFormat(innerW, 6, "KSh "+formatAmount(line.Price), "", 0, "L", false, 0, "")

	if line.Code != "" {
		key := barcode.RegisterCode128(pdf, line.Code)
		barcode.Barcode(pdf, key, innerX, y+15, innerW, 12, false)

		pdf.SetFont("Helvetica", "", 8)
		pdf.SetXY(innerX, y+27.5)
		pdf.CellFormat(innerW, 4, line.Code, "", 0, "C", false, 0, "")
	}
}

// truncate uses "..." rather than a real ellipsis character — fpdf's
// default Helvetica font uses a legacy single-byte encoding, and a raw
// UTF-8 "…" renders as mojibake without extra transcoding.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-3]) + "..."
}

// formatAmount mimics JS toLocaleString — duplicated from quotes.formatAmount
// (same tiny helper, not worth a shared package for one function).
func formatAmount(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimSuffix(s, ".00")
	intPart, frac, hasFrac := strings.Cut(s, ".")

	var b strings.Builder
	if strings.HasPrefix(intPart, "-") {
		b.WriteByte('-')
		intPart = intPart[1:]
	}
	for i, digit := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(digit)
	}
	if hasFrac {
		b.WriteByte('.')
		b.WriteString(frac)
	}
	return b.String()
}
