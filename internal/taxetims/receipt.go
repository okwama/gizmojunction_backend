package taxetims

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-pdf/fpdf"
	"github.com/jackc/pgx/v5/pgxpool"
	qrcode "github.com/skip2/go-qrcode"

	"gizmojunction/backend/internal/auth"
	"gizmojunction/backend/internal/storage"
)

// ReceiptDeps carries what receipt generation needs beyond the invoice
// repo: the Neon pool for product/customer names (orders and tax_invoices
// live in Supabase until the Phase 6 cutover, products/profiles in Neon)
// and R2 storage for the PDF.
type ReceiptDeps struct {
	Repo    *Repo
	Catalog *pgxpool.Pool
	Store   *storage.Client
}

type GenerateReceiptInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		InvoiceID string `json:"invoice_id"`
	}
}

type GenerateReceiptOutput struct {
	Body struct {
		Status string `json:"status"`
		Path   string `json:"path"`
	}
}

// RegisterReceipts wires the admin endpoint the orders page uses to
// (re)generate a receipt PDF on demand — the Phase 6 replacement for
// invoking the generate-tax-receipt Deno function.
func RegisterReceipts(api huma.API, deps ReceiptDeps, authSvc *auth.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "admin-generate-tax-receipt",
		Method:      http.MethodPost,
		Path:        "/v1/admin/tax/receipts",
		Summary:     "Generate (or regenerate) a tax receipt PDF for an invoice (admin only)",
	}, func(ctx context.Context, input *GenerateReceiptInput) (*GenerateReceiptOutput, error) {
		if _, err := authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
			return nil, err
		}
		if input.Body.InvoiceID == "" {
			return nil, huma.Error400BadRequest("invoice_id is required")
		}
		path, err := GenerateReceipt(ctx, deps, input.Body.InvoiceID)
		if err != nil {
			return nil, huma.Error500InternalServerError("receipt generation failed", err)
		}
		out := &GenerateReceiptOutput{}
		out.Body.Status = "success"
		out.Body.Path = path
		return out, nil
	})
}

type receiptItem struct {
	ProductID *string
	Name      string
	Quantity  int32
	UnitPrice float64
}

type receiptData struct {
	InvoiceID        string
	OrderID          string
	CustomerID       *string
	TaxpayerPIN      string
	BuyerPIN         *string
	ReceiptNumber    *string
	IssuedAt         *time.Time
	CUInvoiceNumber  string
	ReceiptSignature string
	InternalData     *string
	TotalAmount      float64
	TaxAmount        float64
	CustomerName     string
	Items            []receiptItem
}

// GenerateReceipt is the Go port of the generate-tax-receipt Deno function:
// same jsPDF layout (brand header, KRA compliance box, item table, totals,
// verification QR), rendered with fpdf and stored in R2, then the invoice
// row is stamped with the path.
func GenerateReceipt(ctx context.Context, d ReceiptDeps, invoiceID string) (string, error) {
	if d.Store == nil {
		return "", fmt.Errorf("document storage (R2) is not configured")
	}

	data, err := loadReceiptData(ctx, d, invoiceID)
	if err != nil {
		return "", err
	}

	pdfBytes, err := renderReceipt(data)
	if err != nil {
		return "", fmt.Errorf("render receipt: %w", err)
	}

	path := "documents/receipts/" + data.InvoiceID + ".pdf"
	if err := d.Store.Upload(ctx, path, pdfBytes, "application/pdf"); err != nil {
		return "", fmt.Errorf("upload receipt: %w", err)
	}

	if _, err := d.Repo.Pool.Exec(ctx, `UPDATE tax_invoices SET receipt_pdf_path = $2 WHERE id = $1`, data.InvoiceID, path); err != nil {
		return "", fmt.Errorf("stamp receipt path: %w", err)
	}
	return path, nil
}

func loadReceiptData(ctx context.Context, d ReceiptDeps, invoiceID string) (receiptData, error) {
	var data receiptData
	err := d.Repo.Pool.QueryRow(ctx, `
		SELECT ti.id::text, ti.order_id::text, o.customer_id::text, ti.taxpayer_pin, ti.buyer_pin,
			ti.receipt_number, ti.issued_at, COALESCE(ti.cu_invoice_number, ''), COALESCE(ti.receipt_signature, ''),
			ti.internal_data, ti.total_amount::float8, ti.tax_amount::float8
		FROM tax_invoices ti
		JOIN orders o ON o.id = ti.order_id
		WHERE ti.id = $1`, invoiceID).Scan(
		&data.InvoiceID, &data.OrderID, &data.CustomerID, &data.TaxpayerPIN, &data.BuyerPIN,
		&data.ReceiptNumber, &data.IssuedAt, &data.CUInvoiceNumber, &data.ReceiptSignature,
		&data.InternalData, &data.TotalAmount, &data.TaxAmount)
	if err != nil {
		return data, fmt.Errorf("load invoice: %w", err)
	}

	rows, err := d.Repo.Pool.Query(ctx, `
		SELECT product_id::text, quantity, unit_price::float8
		FROM order_items WHERE order_id = $1`, data.OrderID)
	if err != nil {
		return data, fmt.Errorf("load items: %w", err)
	}
	productIDs := []string{}
	for rows.Next() {
		var item receiptItem
		if err := rows.Scan(&item.ProductID, &item.Quantity, &item.UnitPrice); err != nil {
			rows.Close()
			return data, err
		}
		item.Name = "Product"
		if item.ProductID != nil {
			productIDs = append(productIDs, *item.ProductID)
		}
		data.Items = append(data.Items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return data, err
	}

	// Names live in Neon now.
	data.CustomerName = "Valued Customer"
	if d.Catalog != nil {
		if len(productIDs) > 0 {
			names := map[string]string{}
			prows, err := d.Catalog.Query(ctx, `SELECT id::text, name FROM products WHERE id = ANY($1::uuid[])`, productIDs)
			if err == nil {
				for prows.Next() {
					var id, name string
					if prows.Scan(&id, &name) == nil {
						names[id] = name
					}
				}
				prows.Close()
			}
			for i := range data.Items {
				if data.Items[i].ProductID != nil {
					if name, ok := names[*data.Items[i].ProductID]; ok {
						data.Items[i].Name = name
					}
				}
			}
		}
		if data.CustomerID != nil {
			var fullName *string
			if err := d.Catalog.QueryRow(ctx, `SELECT full_name FROM profiles WHERE id = $1`, *data.CustomerID).Scan(&fullName); err == nil && fullName != nil && *fullName != "" {
				data.CustomerName = *fullName
			}
		}
	}
	return data, nil
}

func renderReceipt(data receiptData) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()

	const margin = 20.0
	y := 25.0

	// Brand header
	pdf.SetFont("Helvetica", "", 24)
	pdf.SetTextColor(209, 0, 0)
	pdf.Text(margin, y, "GIZMOJUNCTION")

	pdf.SetFontSize(9)
	pdf.SetTextColor(80, 80, 80)
	pdf.Text(margin, y+7, "Kenya's Premier Tech Marketplace")
	pdf.Text(margin, y+12, "Nairobi CBD, Kenya | +254 700 000 000")
	pdf.Text(margin, y+17, "PIN: "+data.TaxpayerPIN)

	pdf.SetFontSize(16)
	pdf.SetTextColor(0, 0, 0)
	pdf.Text(120, y, "ELECTRONIC TAX INVOICE")
	pdf.SetFontSize(10)
	receiptNo := strings.ToUpper(data.InvoiceID[:8])
	if data.ReceiptNumber != nil && *data.ReceiptNumber != "" {
		receiptNo = *data.ReceiptNumber
	}
	pdf.Text(120, y+8, "Receipt No: "+receiptNo)
	issued := time.Now()
	if data.IssuedAt != nil {
		issued = *data.IssuedAt
	}
	pdf.Text(120, y+13, "Date: "+issued.Format("02/01/2006, 15:04:05"))

	y += 40

	// Customer info
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Text(margin, y, "CUSTOMER DETAILS:")
	pdf.SetFont("Helvetica", "", 11)
	pdf.Text(margin, y+7, data.CustomerName)
	orderRefY := y + 12
	if data.BuyerPIN != nil && *data.BuyerPIN != "" {
		pdf.Text(margin, y+12, "PIN: "+*data.BuyerPIN)
		orderRefY = y + 17
	}
	pdf.Text(margin, orderRefY, "Order Ref: #"+data.OrderID[:8])

	y += 35

	// KRA compliance box
	pdf.SetDrawColor(200, 200, 200)
	pdf.SetFillColor(245, 247, 250)
	pdf.Rect(margin, y, 170, 35, "FD")

	pdf.SetFont("Helvetica", "B", 10)
	pdf.Text(margin+5, y+7, "KRA ETIMS COMPLIANCE DATA")

	pdf.SetFont("Helvetica", "", 9)
	pdf.Text(margin+5, y+15, "CU Invoice Number (CUIN): "+data.CUInvoiceNumber)
	pdf.SetFontSize(7)
	sig := data.ReceiptSignature
	if len(sig) > 120 {
		sig = sig[:120] + "..."
	}
	pdf.Text(margin+5, y+22, "Signature: "+sig)
	pdf.SetFontSize(9)
	internal := "N/A"
	if data.InternalData != nil && *data.InternalData != "" {
		internal = *data.InternalData
	}
	pdf.Text(margin+5, y+30, "Internal Data: "+internal)

	y += 45

	// Item table header
	pdf.SetFillColor(30, 41, 59)
	pdf.Rect(margin, y, 170, 8, "F")
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont("Helvetica", "B", 9)
	pdf.Text(margin+5, y+5.5, "DESCRIPTION")
	pdf.Text(115, y+5.5, "QTY")
	pdf.Text(135, y+5.5, "TAX")
	pdf.Text(155, y+5.5, "UNIT PRICE")
	textRight(pdf, 185, y+5.5, "TOTAL")

	y += 8
	pdf.SetTextColor(0, 0, 0)
	pdf.SetFont("Helvetica", "", 9)

	for _, item := range data.Items {
		y += 8
		name := item.Name
		if len(name) > 50 {
			name = name[:50] + "..."
		}
		pdf.Text(margin+5, y, name)
		pdf.Text(117, y, strconv.Itoa(int(item.Quantity)))
		pdf.Text(137, y, "16%")
		pdf.Text(157, y, formatAmount(item.UnitPrice))
		textRight(pdf, 185, y, formatAmount(item.UnitPrice*float64(item.Quantity)))
		if y > 240 {
			pdf.AddPage()
			y = 25
		}
	}

	y += 15
	pdf.Line(130, y, 190, y)
	y += 8

	pdf.Text(130, y, "Subtotal (Excl. VAT):")
	textRight(pdf, 185, y, formatAmount(data.TotalAmount-data.TaxAmount))

	y += 6
	pdf.Text(130, y, "Total VAT (16%):")
	textRight(pdf, 185, y, formatAmount(data.TaxAmount))

	y += 8
	pdf.SetFont("Helvetica", "B", 12)
	pdf.Text(130, y, "TOTAL AMOUNT (KSH):")
	textRight(pdf, 185, y, formatAmount(data.TotalAmount))

	// KRA verification QR code (bottom left)
	verificationURL := "https://etims.kra.go.ke/verify?cuin=" + data.CUInvoiceNumber + "&sig=" + data.ReceiptSignature
	if qrPNG, err := qrcode.Encode(verificationURL, qrcode.Medium, 200); err == nil {
		opts := fpdf.ImageOptions{ImageType: "PNG"}
		pdf.RegisterImageOptionsReader("kra-qr", opts, bytes.NewReader(qrPNG))
		pdf.ImageOptions("kra-qr", margin, 235, 35, 35, false, opts, 0, "")
	}

	pdf.SetFont("Helvetica", "I", 8)
	pdf.SetTextColor(0, 0, 0)
	pdf.Text(margin, 275, "Scan QR code to verify this tax invoice on KRA portal")

	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(150, 150, 150)
	footer := "GizmoJunction E-Commerce Platform"
	pageW, _ := pdf.GetPageSize()
	pdf.Text(pageW/2-pdf.GetStringWidth(footer)/2, 285, footer)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func textRight(pdf *fpdf.Fpdf, x, y float64, s string) {
	pdf.Text(x-pdf.GetStringWidth(s), y, s)
}

// formatAmount mimics JS toLocaleString: comma-grouped thousands, decimals
// only when the value has them (same helper as the ERP LPO port).
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
