package taxetims

import "time"

// TaxInvoice mirrors the tax_invoices table (supabase/migrations/20260314170000_etims_integration.sql).
// Columns unused by the old mocked create-tax-invoice function (CUID,
// RequestPayload, ResponsePayload, AttemptCount) are exactly what the real
// OSCU integration needs.
type TaxInvoice struct {
	ID               string     `db:"id"`
	OrderID          string     `db:"order_id"`
	Status           string     `db:"status"` // PENDING / ISSUED / FAILED / FAILED_FINAL
	TaxpayerPIN      string     `db:"taxpayer_pin"`
	BuyerPIN         *string    `db:"buyer_pin"`
	CUID             *string    `db:"cu_id"`
	CUInvoiceNumber  *string    `db:"cu_invoice_number"`
	ReceiptSignature *string    `db:"receipt_signature"`
	InternalData     *string    `db:"internal_data"`
	ReceiptNumber    *string    `db:"receipt_number"`
	TotalAmount      float64    `db:"total_amount"`
	TaxAmount        float64    `db:"tax_amount"`
	RequestPayload   []byte     `db:"request_payload"`
	ResponsePayload  []byte     `db:"response_payload"`
	AttemptCount     int32      `db:"attempt_count"`
	LastAttemptAt    *time.Time `db:"last_attempt_at"`
	IssuedAt         *time.Time `db:"issued_at"`
	ErrorMessage     *string    `db:"error_message"`
	ReceiptPDFPath   *string    `db:"receipt_pdf_path"`
}

// OrderLine is the subset of order_items (joined with products) needed to
// build an OSCU sales payload line item.
type OrderLine struct {
	ProductName string   `db:"name"`
	SKU         *string  `db:"sku"`
	Quantity    int32    `db:"quantity"`
	UnitPrice   float64  `db:"unit_price"`
	TaxClass    *string  `db:"tax_class"`
	TaxRate     *float64 `db:"tax_rate"`
}

// OrderForInvoice is the order data needed to submit an OSCU sale.
type OrderForInvoice struct {
	ID            string    `db:"id"`
	TotalAmount   float64   `db:"total_amount"`
	PaymentMethod string    `db:"payment_method"`
	KRAPIN        *string   `db:"kra_pin"`
	CreatedAt     time.Time `db:"created_at"`
}

// DeviceConfig mirrors the kra_device_config table
// (supabase/migrations/20260714000000_kra_device_config.sql) — the result
// of the one-time OSCU device initialization call.
type DeviceConfig struct {
	Environment string  `db:"environment"`
	TIN         string  `db:"tin"`
	BhfID       string  `db:"bhf_id"`
	DvcSrlNo    string  `db:"dvc_srl_no"`
	CMCKey      string  `db:"cmc_key"`
	SdcID       *string `db:"sdc_id"`
	MrcNo       *string `db:"mrc_no"`
}
