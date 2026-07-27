package quotes

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-pdf/fpdf"
)

// renderQuote mirrors erp.renderLPO's layout (same A4 page, coordinates,
// fonts) — the doc's own suggestion to clone the LPO template rather than
// build a new document style from scratch.
func renderQuote(q Quote) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()

	const margin = 20.0
	y := 30.0

	pdf.SetFont("Helvetica", "", 22)
	pdf.SetTextColor(30, 41, 59)
	pdf.Text(margin, y, "GIZMOJUNCTION")

	pdf.SetFontSize(10)
	pdf.SetTextColor(100, 116, 139)
	pdf.Text(margin, y+8, "Quotation")

	pdf.SetFontSize(12)
	pdf.SetTextColor(30, 41, 59)
	pdf.Text(140, y, "Quote No: QT-"+strings.ToUpper(q.ID[:6]))
	pdf.Text(140, y+7, "Date: "+q.CreatedAt.Format("02/01/2006"))
	if q.ValidUntil != nil {
		pdf.Text(140, y+14, "Valid Until: "+q.ValidUntil.Format("02/01/2006"))
	}

	y += 30
	pdf.Line(margin, y, 190, y)
	y += 15

	pdf.SetFontSize(14)
	pdf.Text(margin, y, "PREPARED FOR")
	pdf.SetFontSize(11)
	pdf.Text(margin, y+7, q.CustomerName)
	if q.CustomerEmail != nil {
		pdf.Text(margin, y+14, *q.CustomerEmail)
	}
	if q.CustomerPhone != nil {
		pdf.Text(margin, y+21, *q.CustomerPhone)
	}

	y += 40

	pdf.SetFont("Helvetica", "B", 11)
	pdf.Text(margin, y, "Item / SKU")
	pdf.Text(110, y, "Qty")
	pdf.Text(130, y, "Unit Price")
	pdf.Text(165, y, "Line Total")
	y += 5
	pdf.Line(margin, y, 190, y)
	y += 10

	pdf.SetFont("Helvetica", "", 11)
	for _, item := range q.Items {
		label := item.Name
		if item.SKU != nil && *item.SKU != "" {
			label = fmt.Sprintf("%s (%s)", item.Name, *item.SKU)
		}
		pdf.Text(margin, y, label)
		pdf.Text(110, y, strconv.Itoa(int(item.Quantity)))
		pdf.Text(130, y, formatAmount(item.UnitPrice))
		pdf.Text(165, y, formatAmount(float64(item.Quantity)*item.UnitPrice))
		y += 10
	}

	y += 5
	pdf.Line(margin, y, 190, y)
	y += 12

	pdf.SetFont("Helvetica", "", 11)
	pdf.Text(130, y, "Subtotal (KES):")
	pdf.Text(165, y, formatAmount(q.Subtotal))
	y += 8
	pdf.Text(130, y, "VAT (KES):")
	pdf.Text(165, y, formatAmount(q.VatAmount))
	y += 10

	pdf.SetFont("Helvetica", "B", 14)
	pdf.Text(130, y, "Total (KES):")
	pdf.Text(165, y, formatAmount(q.TotalAmount))

	if q.Notes != nil && *q.Notes != "" {
		y += 20
		pdf.SetFont("Helvetica", "", 10)
		pdf.SetTextColor(100, 116, 139)
		pdf.Text(margin, y, "Notes: "+*q.Notes)
	}

	pdf.SetFont("Helvetica", "I", 10)
	pdf.SetTextColor(100, 116, 139)
	pdf.Text(margin, 280, "This quotation is valid until the date shown above and does not require a signature.")

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// formatAmount mimics JS toLocaleString: comma-grouped thousands, decimals
// only when the value has them — duplicated from erp.formatAmount (same
// tiny helper, not worth a shared package for one function).
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
