package erp

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-pdf/fpdf"
)

// renderLPO is a faithful port of the retired generate-lpo Deno function's
// jsPDF layout: same A4 page, coordinates, sizes and colors, so the
// document looks identical to what suppliers have been receiving.
func renderLPO(po lpoData) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()

	const margin = 20.0
	y := 30.0

	// Branding
	pdf.SetFont("Helvetica", "", 22)
	pdf.SetTextColor(30, 41, 59)
	pdf.Text(margin, y, "GIZMOJUNCTION")

	pdf.SetFontSize(10)
	pdf.SetTextColor(100, 116, 139)
	pdf.Text(margin, y+8, "Local Purchase Order (LPO)")

	// PO info
	pdf.SetFontSize(12)
	pdf.SetTextColor(30, 41, 59)
	pdf.Text(140, y, "PO Number: PO-"+strings.ToUpper(po.ID[:6]))
	pdf.Text(140, y+7, "Date: "+po.CreatedAt.Format("02/01/2006"))

	y += 30
	pdf.Line(margin, y, 190, y)
	y += 15

	// Supplier info
	pdf.SetFontSize(14)
	pdf.Text(margin, y, "SUPPLIER")
	pdf.SetFontSize(11)
	pdf.Text(margin, y+7, po.SupplierName)
	pdf.Text(margin, y+14, po.SupplierEmail)
	pdf.Text(margin, y+21, po.SupplierPhone)

	y += 40

	// Items table
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Text(margin, y, "Item / SKU")
	pdf.Text(120, y, "Qty")
	pdf.Text(140, y, "Unit Cost")
	pdf.Text(170, y, "Line Total")
	y += 5
	pdf.Line(margin, y, 190, y)
	y += 10

	pdf.SetFont("Helvetica", "", 11)
	for _, item := range po.Items {
		pdf.Text(margin, y, fmt.Sprintf("%s (%s)", item.Name, item.SKU))
		pdf.Text(120, y, strconv.Itoa(int(item.Quantity)))
		pdf.Text(140, y, formatAmount(item.UnitCost))
		pdf.Text(170, y, formatAmount(float64(item.Quantity)*item.UnitCost))
		y += 10
	}

	y += 10
	pdf.Line(margin, y, 190, y)
	y += 15

	// Total
	pdf.SetFont("Helvetica", "B", 14)
	pdf.Text(120, y, "Grand Total (KES):")
	pdf.Text(170, y, formatAmount(po.TotalAmount))

	// Footer
	pdf.SetFont("Helvetica", "I", 10)
	pdf.Text(margin, 280, "This is a computer-generated document and does not require a signature.")

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// formatAmount mimics JS toLocaleString: comma-grouped thousands, decimals
// only when the value has them.
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
