// Commercial/pro-forma invoice generation — distinct from the KRA eTIMS
// fiscal tax invoice (internal/taxetims), which has its own compliance
// fields (CUIN, QR, taxpayer PIN) and already exists. This is the B2B-style
// document with payment terms, VAT breakdown by rate, and a bill-to block,
// stored in R2 and downloaded the same way LPOs already are.
package orders

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"gizmojunction/backend/internal/storage"
)

type invoiceLine struct {
	Name      string
	Quantity  int32
	UnitPrice float64
	TaxClass  string
}

type invoiceData struct {
	OrderID       string
	CreatedAt     time.Time
	CustomerName  string
	CustomerEmail string
	Address       string
	PaymentMethod string
	PaymentStatus string
	PaymentTerms  string
	ShippingFee   float64
	Items         []invoiceLine
}

// GenerateInvoice renders the invoice PDF and stores it in R2, mirroring
// erp.GenerateLPO's shape (load data, render, upload, return the path).
func (r *Repo) GenerateInvoice(ctx context.Context, store *storage.Client, orderID string) (string, error) {
	if store == nil {
		return "", fmt.Errorf("document storage (R2) is not configured")
	}
	data, err := r.loadInvoiceData(ctx, orderID)
	if err != nil {
		return "", err
	}
	pdf, err := renderInvoice(data)
	if err != nil {
		return "", fmt.Errorf("render invoice: %w", err)
	}
	path := "documents/invoices/INV-" + strings.ToUpper(data.OrderID[:8]) + ".pdf"
	if err := store.Upload(ctx, path, pdf, "application/pdf"); err != nil {
		return "", fmt.Errorf("upload invoice: %w", err)
	}
	return path, nil
}

func (r *Repo) loadInvoiceData(ctx context.Context, orderID string) (invoiceData, error) {
	var d invoiceData
	var shippingRaw []byte
	var paymentMethod, paymentStatus, paymentTerms *string

	err := r.db.QueryRow(ctx, `
		SELECT id::text, created_at, shipping_address, payment_method, payment_status, payment_terms, COALESCE(shipping_fee, 0)::float8
		FROM orders WHERE id = $1`, orderID).
		Scan(&d.OrderID, &d.CreatedAt, &shippingRaw, &paymentMethod, &paymentStatus, &paymentTerms, &d.ShippingFee)
	if err != nil {
		return d, fmt.Errorf("load order: %w", err)
	}
	if paymentMethod != nil {
		d.PaymentMethod = *paymentMethod
	}
	if paymentStatus != nil {
		d.PaymentStatus = *paymentStatus
	}
	if paymentTerms != nil && *paymentTerms != "" {
		d.PaymentTerms = *paymentTerms
	} else {
		d.PaymentTerms = "Due on receipt"
	}

	var sa struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Email     string `json:"email"`
		Address   string `json:"address"`
		City      string `json:"city"`
		County    string `json:"county"`
		Company   string `json:"company"`
	}
	if len(shippingRaw) > 0 && string(shippingRaw) != "null" {
		_ = json.Unmarshal(shippingRaw, &sa)
	}
	d.CustomerName = strings.TrimSpace(sa.FirstName + " " + sa.LastName)
	if sa.Company != "" {
		d.CustomerName = sa.Company + " (Attn: " + d.CustomerName + ")"
	}
	if d.CustomerName == "" {
		d.CustomerName = "Valued Customer"
	}
	d.CustomerEmail = sa.Email
	d.Address = strings.Trim(strings.Join([]string{sa.Address, sa.City, sa.County}, ", "), ", ")

	rows, err := r.db.Query(ctx, `
		SELECT oi.quantity, oi.unit_price::float8, COALESCE(oi.tax_class, 'VAT_16'), p.name
		FROM order_items oi JOIN products p ON p.id = oi.product_id
		WHERE oi.order_id = $1`, orderID)
	if err != nil {
		return d, fmt.Errorf("load items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var line invoiceLine
		if err := rows.Scan(&line.Quantity, &line.UnitPrice, &line.TaxClass, &line.Name); err != nil {
			return d, err
		}
		d.Items = append(d.Items, line)
	}
	return d, rows.Err()
}

// vatOrder is the fixed print order for VAT groups — map iteration order in
// Go is randomized, and a receipt/invoice must render the same rate in the
// same place every time.
var vatOrder = []string{"VAT_16", "VAT_8", "VAT_0", "EXEMPT", "NOT_APPLICABLE"}
var vatRates = map[string]float64{"VAT_16": 0.16, "VAT_8": 0.08}
var vatLabels = map[string]string{
	"VAT_16":         "Standard-rated (16%)",
	"VAT_8":          "Reduced-rate (8%)",
	"VAT_0":          "Zero-rated",
	"EXEMPT":         "Exempt",
	"NOT_APPLICABLE": "Not subject to VAT",
}

type vatGroup struct {
	Label string
	Gross float64
	VAT   float64
}

// vatBreakdown groups line totals (plus the shipping fee, treated as a
// standard-rated service) by each item's snapshotted tax_class — the same
// logic as the admin order-detail page's client-side vatBreakdown(), kept
// in sync deliberately since both must agree with what actually gets
// invoiced.
func vatBreakdown(items []invoiceLine, shippingFee float64) ([]vatGroup, float64) {
	groups := map[string]*vatGroup{}
	add := func(taxClass string, gross float64) {
		rate := vatRates[taxClass]
		var vat float64
		if rate > 0 {
			vat = gross * (rate / (1 + rate))
		}
		g, ok := groups[taxClass]
		if !ok {
			label := vatLabels[taxClass]
			if label == "" {
				label = taxClass
			}
			g = &vatGroup{Label: label}
			groups[taxClass] = g
		}
		g.Gross += gross
		g.VAT += vat
	}
	for _, item := range items {
		add(item.TaxClass, float64(item.Quantity)*item.UnitPrice)
	}
	if shippingFee > 0 {
		add("VAT_16", shippingFee)
	}

	var out []vatGroup
	var totalVAT float64
	for _, key := range vatOrder {
		if g, ok := groups[key]; ok {
			out = append(out, *g)
			totalVAT += g.VAT
		}
	}
	return out, totalVAT
}

func renderInvoice(d invoiceData) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()

	const margin = 20.0
	y := 25.0

	pdf.SetFont("Helvetica", "", 22)
	pdf.SetTextColor(30, 41, 59)
	pdf.Text(margin, y, "GIZMOJUNCTION")
	pdf.SetFontSize(9)
	pdf.SetTextColor(100, 116, 139)
	pdf.Text(margin, y+7, "Nairobi CBD, Kenya | sales@gizmojunction.com | www.gizmojunction.com")

	pdf.SetFontSize(18)
	pdf.SetTextColor(30, 41, 59)
	pdf.Text(130, y, "INVOICE")
	pdf.SetFontSize(10)
	invoiceNo := "INV-" + strings.ToUpper(d.OrderID[:8])
	pdf.Text(130, y+8, invoiceNo)
	pdf.Text(130, y+13, "Date: "+d.CreatedAt.Format("02/01/2006"))

	y += 30
	pdf.Line(margin, y, 190, y)
	y += 12

	pdf.SetFont("Helvetica", "B", 11)
	pdf.SetTextColor(30, 41, 59)
	pdf.Text(margin, y, "BILL TO")
	pdf.SetFont("Helvetica", "", 10)
	pdf.Text(margin, y+7, d.CustomerName)
	if d.CustomerEmail != "" {
		pdf.Text(margin, y+13, d.CustomerEmail)
	}
	if d.Address != "" {
		pdf.Text(margin, y+19, d.Address)
	}

	pdf.SetFont("Helvetica", "B", 10)
	pdf.Text(130, y, "PAYMENT TERMS")
	pdf.SetFont("Helvetica", "", 10)
	pdf.Text(130, y+7, d.PaymentTerms)
	pdf.Text(130, y+13, "Method: "+strings.ToUpper(orDash(d.PaymentMethod)))
	pdf.Text(130, y+19, "Status: "+strings.ToUpper(orDash(d.PaymentStatus)))

	y += 32

	pdf.SetFillColor(30, 41, 59)
	pdf.Rect(margin, y, 170, 8, "F")
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont("Helvetica", "B", 9)
	pdf.Text(margin+3, y+5.5, "DESCRIPTION")
	pdf.Text(115, y+5.5, "QTY")
	pdf.Text(135, y+5.5, "TAX")
	pdf.Text(153, y+5.5, "UNIT PRICE")
	textRight(pdf, 187, y+5.5, "TOTAL")

	y += 8
	pdf.SetTextColor(0, 0, 0)
	pdf.SetFont("Helvetica", "", 9)
	var subtotal float64
	for _, item := range d.Items {
		y += 8
		name := item.Name
		if len(name) > 46 {
			name = name[:46] + "..."
		}
		lineTotal := float64(item.Quantity) * item.UnitPrice
		subtotal += lineTotal
		pdf.Text(margin+3, y, name)
		pdf.Text(117, y, strconv.Itoa(int(item.Quantity)))
		pdf.Text(133, y, taxRateLabel(item.TaxClass))
		pdf.Text(153, y, formatAmount(item.UnitPrice))
		textRight(pdf, 187, y, formatAmount(lineTotal))
		if y > 245 {
			pdf.AddPage()
			y = 25
		}
	}

	y += 12
	pdf.Line(125, y, 190, y)
	y += 8

	groups, totalVAT := vatBreakdown(d.Items, d.ShippingFee)
	pdf.SetFontSize(9)
	pdf.Text(125, y, "Subtotal:")
	textRight(pdf, 187, y, formatAmount(subtotal))
	y += 6
	if d.ShippingFee > 0 {
		pdf.Text(125, y, "Shipping:")
		textRight(pdf, 187, y, formatAmount(d.ShippingFee))
		y += 6
	}
	for _, g := range groups {
		pdf.Text(125, y, "VAT — "+g.Label+":")
		textRight(pdf, 187, y, formatAmount(g.VAT))
		y += 6
	}

	y += 3
	pdf.SetFont("Helvetica", "B", 12)
	pdf.Text(125, y, "TOTAL (KES):")
	textRight(pdf, 187, y, formatAmount(subtotal+d.ShippingFee))
	_ = totalVAT

	pdf.SetFont("Helvetica", "I", 8)
	pdf.SetTextColor(100, 116, 139)
	pdf.Text(margin, 280, "This is a commercial invoice for record-keeping purposes and is not a KRA-fiscalized tax receipt.")

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func taxRateLabel(taxClass string) string {
	switch taxClass {
	case "VAT_16":
		return "16%"
	case "VAT_8":
		return "8%"
	case "VAT_0":
		return "0%"
	case "EXEMPT":
		return "EX"
	default:
		return "N/A"
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func textRight(pdf *fpdf.Fpdf, x, y float64, s string) {
	pdf.Text(x-pdf.GetStringWidth(s), y, s)
}

// formatAmount mimics JS toLocaleString: comma-grouped thousands, decimals
// only when the value has them (same helper duplicated in erp/lpo.go and
// taxetims/receipt.go — small enough that a shared package isn't worth it).
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
