package taxetims

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"gizmojunction/backend/internal/jobs"
)

// TaxInvoiceSubmitArgs is enqueued once per order after payment
// confirmation (or by an admin manual issue/retry). Kind must be unique
// across all workers registered in jobs.NewClient.
type TaxInvoiceSubmitArgs struct {
	TaxInvoiceID string `json:"tax_invoice_id"`
	OrderID      string `json:"order_id"`
}

func (TaxInvoiceSubmitArgs) Kind() string { return "tax_invoice_submit" }

// InsertOpts caps retries at 5 attempts (BRD FR-OSCU-24: "After 5 failed
// retry attempts, escalate to FAILED_FINAL"), using river's own backoff
// scheduler instead of a separate cron-based retry function — the same
// mechanism every other job in this backend already relies on.
func (TaxInvoiceSubmitArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 5}
}

type TaxInvoiceSubmitWorker struct {
	river.WorkerDefaults[TaxInvoiceSubmitArgs]
	Repo       *Repo
	Client     *Client
	Email      *jobs.EmailSender
	AdminEmail string

	// Receipt carries what in-process PDF generation needs (Neon pool for
	// product/customer names, R2 storage) — the Phase 6 replacement for
	// calling the generate-tax-receipt Deno function over HTTP. Optional:
	// if storage isn't configured, PDF generation is skipped (logged)
	// rather than erroring — the BRD's own risk table treats a receipt PDF
	// failure as independent of the already-ISSUED tax_status, so this
	// must never block/retry the job.
	Receipt ReceiptDeps
}

func (w *TaxInvoiceSubmitWorker) Work(ctx context.Context, job *river.Job[TaxInvoiceSubmitArgs]) error {
	deviceCfg, cfgErr := w.Repo.GetDeviceConfig(ctx)
	if cfgErr != nil {
		// Not configured (no real KRA credentials yet in this environment,
		// or the device simply hasn't been registered) — log and succeed
		// rather than error, so the job never occupies river's retry loop
		// pointlessly. The row stays PENDING; an admin re-enqueues once
		// device init has actually happened. Mirrors EmailSender.Send's
		// graceful no-op when RESEND_API_KEY is unset.
		fmt.Printf("kra_device_config not set; tax invoice %s for order %s left PENDING (OSCU not configured)\n", job.Args.TaxInvoiceID, job.Args.OrderID)
		return nil
	}

	if err := w.Repo.MarkAttempt(ctx, job.Args.TaxInvoiceID); err != nil {
		return fmt.Errorf("mark attempt: %w", err)
	}

	order, err := w.Repo.LoadOrder(ctx, job.Args.OrderID)
	if err != nil {
		return fmt.Errorf("load order: %w", err)
	}
	lines, err := w.Repo.LoadOrderLines(ctx, job.Args.OrderID)
	if err != nil {
		return fmt.Errorf("load order lines: %w", err)
	}

	req, reqPayload, err := buildSalesRequest(deviceCfg, order, lines)
	if err != nil {
		return w.terminalOrRetry(ctx, job, fmt.Sprintf("build OSCU payload: %v", err))
	}

	resp, err := w.Client.SubmitSale(ctx, deviceCfg.CMCKey, req)
	if err != nil {
		return w.terminalOrRetry(ctx, job, err.Error())
	}

	respPayload, _ := json.Marshal(resp)
	if err := w.Repo.MarkIssued(ctx, job.Args.TaxInvoiceID, job.Args.OrderID, resp.Data.CuInvcNo, resp.Data.RcptSign, resp.Data.IntrlData, resp.Data.SdcID, reqPayload, respPayload); err != nil {
		return fmt.Errorf("mark issued: %w", err)
	}

	// Receipt PDF generation, now in-process (Phase 6) — deliberately
	// fire-and-forget: BRD risk table notes "Receipt PDF generation fails
	// after successful OSCU response" must not affect the already-ISSUED
	// tax_status.
	go w.generateReceipt(job.Args.TaxInvoiceID)

	return nil
}

func (w *TaxInvoiceSubmitWorker) generateReceipt(invoiceID string) {
	if w.Receipt.Store == nil {
		fmt.Printf("R2 storage not configured; receipt PDF generation skipped for invoice %s\n", invoiceID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := GenerateReceipt(ctx, w.Receipt, invoiceID); err != nil {
		fmt.Printf("receipt generation failed for invoice %s: %v\n", invoiceID, err)
	}
}

// terminalOrRetry marks the invoice FAILED and lets river retry with
// backoff, unless this was the last allowed attempt — in which case it
// marks FAILED_FINAL, emails the admin, and returns nil so river doesn't
// keep retrying a job whose business outcome is already terminal.
func (w *TaxInvoiceSubmitWorker) terminalOrRetry(ctx context.Context, job *river.Job[TaxInvoiceSubmitArgs], errMsg string) error {
	if job.Attempt >= job.MaxAttempts {
		if err := w.Repo.MarkFailedFinal(ctx, job.Args.TaxInvoiceID, job.Args.OrderID, errMsg); err != nil {
			return fmt.Errorf("mark failed final: %w", err)
		}
		if w.Email != nil && w.AdminEmail != "" {
			_ = w.Email.Send(ctx, jobs.EmailPayload{
				From:    "Gizmo Junction <noreply@notify.gizmojunction.com>",
				To:      []string{w.AdminEmail},
				Subject: fmt.Sprintf("eTIMS tax invoice failed permanently (order %s)", job.Args.OrderID[:8]),
				HTML:    fmt.Sprintf("<p>Order <strong>%s</strong> failed KRA OSCU submission after %d attempts.</p><p>Last error: %s</p>", job.Args.OrderID, job.MaxAttempts, errMsg),
			})
		}
		return nil
	}

	if err := w.Repo.MarkFailed(ctx, job.Args.TaxInvoiceID, job.Args.OrderID, errMsg); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	return fmt.Errorf("oscu submission failed (attempt %d/%d): %s", job.Attempt, job.MaxAttempts, errMsg)
}

// buildSalesRequest constructs the OSCU sales payload per the BRD's
// Section 5.1 field mapping. Returns the request alongside its own JSON
// encoding for the request_payload audit column.
func buildSalesRequest(cfg DeviceConfig, order OrderForInvoice, lines []OrderLine) (SalesRequest, []byte, error) {
	if len(lines) == 0 {
		return SalesRequest{}, nil, fmt.Errorf("order has no line items")
	}

	items := make([]SalesLineItem, 0, len(lines))
	var totTaxblAmt, totTaxAmt float64
	for i, l := range lines {
		rate := 16.0
		if l.TaxRate != nil {
			rate = *l.TaxRate
		}
		taxTyCd := "B" // standard 16% VAT
		if l.TaxClass != nil && strings.Contains(strings.ToUpper(*l.TaxClass), "ZERO") {
			taxTyCd = "C"
		} else if l.TaxClass != nil && strings.Contains(strings.ToUpper(*l.TaxClass), "EXEMPT") {
			taxTyCd = "A"
		}

		supplyAmt := l.UnitPrice * float64(l.Quantity)
		taxAmt := supplyAmt * rate / (100 + rate) // rate applied on VAT-inclusive price, matching orders.total_amount being inclusive
		itemCd := order.ID
		if l.SKU != nil {
			itemCd = *l.SKU
		}

		items = append(items, SalesLineItem{
			ItemSeq: int32(i + 1),
			ItemCd:  itemCd,
			ItemNm:  l.ProductName,
			Qty:     float64(l.Quantity),
			Prc:     l.UnitPrice,
			SplyAmt: supplyAmt,
			TaxTyCd: taxTyCd,
			TaxAmt:  taxAmt,
			TotAmt:  supplyAmt,
		})
		totTaxblAmt += supplyAmt - taxAmt
		totTaxAmt += taxAmt
	}

	custTin := ""
	if order.KRAPIN != nil {
		custTin = *order.KRAPIN
	}

	req := SalesRequest{
		TIN:         cfg.TIN,
		BhfID:       cfg.BhfID,
		InvcNo:      order.ID,
		CustTin:     custTin,
		SalesDt:     order.CreatedAt.Format("20060102"),
		TotItemCnt:  len(items),
		TaxblAmtB:   totTaxblAmt,
		TaxAmtB:     totTaxAmt,
		TotTaxblAmt: totTaxblAmt,
		TotTaxAmt:   totTaxAmt,
		TotAmt:      order.TotalAmount,
		PmtTyCd:     paymentMethodCode(order.PaymentMethod),
		ItemList:    items,
	}

	payload, err := json.Marshal(req)
	return req, payload, err
}

func paymentMethodCode(method string) string {
	switch strings.ToLower(method) {
	case "mpesa":
		return "05" // mobile money, per OSCU payment type codes
	case "paystack_card", "card":
		return "02" // card
	default:
		return "01" // cash/other
	}
}
