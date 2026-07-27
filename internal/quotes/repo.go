// Package quotes covers ADMIN_RETAIL_OPS_REVIEW.md §3: quote -> PDF ->
// convert-to-order, the B2B sales motion (quotes routinely discount off
// list price, which is the whole point of a quote).
//
// Line items live in their own quote_items table, matching every other
// line-item domain in this schema (order_items, return_items,
// purchase_order_items) rather than a jsonb blob.
//
// Converting an accepted quote to an order does NOT go through
// orders.Repo.CreateOrder — that function deliberately ignores
// client-supplied prices and always recomputes from the current catalog
// (so a tampered checkout can't buy at its own price), which is exactly
// wrong for a quote's negotiated prices. ConvertToOrder below inserts
// directly into orders/order_items instead, composing from outside the
// orders package rather than adding a quote-specific escape hatch into it.
package quotes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

type QuoteItemInput struct {
	ProductID *string
	Name      string
	SKU       string
	Quantity  int32
	UnitPrice float64
	TaxClass  string
}

type NewQuote struct {
	CustomerName  string
	CustomerEmail string
	CustomerPhone string
	Notes         string
	ValidUntil    *time.Time
	Items         []QuoteItemInput
}

type QuoteItem struct {
	ID        string  `db:"id" json:"id"`
	ProductID *string `db:"product_id" json:"product_id,omitempty"`
	Name      string  `db:"name" json:"name"`
	SKU       *string `db:"sku" json:"sku,omitempty"`
	Quantity  int32   `db:"quantity" json:"quantity"`
	UnitPrice float64 `db:"unit_price" json:"unit_price"`
	TaxClass  string  `db:"tax_class" json:"tax_class"`
}

type Quote struct {
	ID               string      `db:"id" json:"id"`
	CustomerName     string      `db:"customer_name" json:"customer_name"`
	CustomerEmail    *string     `db:"customer_email" json:"customer_email,omitempty"`
	CustomerPhone    *string     `db:"customer_phone" json:"customer_phone,omitempty"`
	Subtotal         float64     `db:"subtotal" json:"subtotal"`
	VatAmount        float64     `db:"vat_amount" json:"vat_amount"`
	TotalAmount      float64     `db:"total_amount" json:"total_amount"`
	ValidUntil       *time.Time  `db:"valid_until" json:"valid_until,omitempty"`
	Status           string      `db:"status" json:"status"`
	Notes            *string     `db:"notes" json:"notes,omitempty"`
	ConvertedOrderID *string     `db:"converted_order_id" json:"converted_order_id,omitempty"`
	CreatedAt        time.Time   `db:"created_at" json:"created_at"`
	UpdatedAt        time.Time   `db:"updated_at" json:"updated_at"`
	Items            []QuoteItem `db:"-" json:"items"`
}

// VAT convention mirrors the order-detail page's vatBreakdown(): unit
// prices are VAT-inclusive, so vat = gross * rate/(1+rate).
var vatRates = map[string]float64{"VAT_16": 0.16, "VAT_8": 0.08}

func vatFor(taxClass string, gross float64) float64 {
	rate := vatRates[taxClass]
	if rate == 0 {
		return 0
	}
	return gross * (rate / (1 + rate))
}

const quoteColumns = `id::text, customer_name, customer_email, customer_phone, subtotal, vat_amount, total_amount,
	valid_until, status, notes, converted_order_id::text, created_at, updated_at`

// CreateQuote computes subtotal (net of VAT) / vat_amount / total_amount
// server-side from the submitted line items — the admin builder shows a
// live preview of the same math, but this is the authoritative figure.
func (r *Repo) CreateQuote(ctx context.Context, in NewQuote) (string, error) {
	if len(in.Items) == 0 {
		return "", fmt.Errorf("at least one item is required")
	}
	if in.CustomerName == "" {
		return "", fmt.Errorf("customer name is required")
	}

	var subtotal, vatAmount float64
	for _, item := range in.Items {
		if item.Quantity <= 0 {
			return "", fmt.Errorf("invalid quantity for %s", item.Name)
		}
		gross := item.UnitPrice * float64(item.Quantity)
		vat := vatFor(item.TaxClass, gross)
		subtotal += gross - vat
		vatAmount += vat
	}
	totalAmount := subtotal + vatAmount

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var quoteID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO quotes (customer_name, customer_email, customer_phone, subtotal, vat_amount, total_amount, valid_until, notes)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), $4, $5, $6, $7, NULLIF($8, ''))
		RETURNING id::text`,
		in.CustomerName, in.CustomerEmail, in.CustomerPhone, subtotal, vatAmount, totalAmount, in.ValidUntil, in.Notes,
	).Scan(&quoteID); err != nil {
		return "", fmt.Errorf("insert quote: %w", err)
	}

	for _, item := range in.Items {
		taxClass := item.TaxClass
		if taxClass == "" {
			taxClass = "VAT_16"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO quote_items (quote_id, product_id, name, sku, quantity, unit_price, tax_class)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7)`,
			quoteID, item.ProductID, item.Name, item.SKU, item.Quantity, item.UnitPrice, taxClass); err != nil {
			return "", fmt.Errorf("insert quote item: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return quoteID, nil
}

func (r *Repo) ListQuotes(ctx context.Context) ([]Quote, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+quoteColumns+` FROM quotes ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[Quote])
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []Quote{}
	}
	if err := r.attachQuoteItems(ctx, list); err != nil {
		return nil, err
	}
	return list, nil
}

func (r *Repo) GetQuote(ctx context.Context, id string) (*Quote, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+quoteColumns+` FROM quotes WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	q, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Quote])
	if err != nil {
		return nil, err
	}
	list := []Quote{q}
	if err := r.attachQuoteItems(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

type quoteItemRow struct {
	ID        string  `db:"id"`
	QuoteID   string  `db:"quote_id"`
	ProductID *string `db:"product_id"`
	Name      string  `db:"name"`
	SKU       *string `db:"sku"`
	Quantity  int32   `db:"quantity"`
	UnitPrice float64 `db:"unit_price"`
	TaxClass  string  `db:"tax_class"`
}

func (r *Repo) attachQuoteItems(ctx context.Context, list []Quote) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	for i, q := range list {
		ids[i] = q.ID
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id::text, quote_id::text, product_id::text, name, sku, quantity, unit_price, tax_class
		FROM quote_items WHERE quote_id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return err
	}
	itemRows, err := pgx.CollectRows(rows, pgx.RowToStructByName[quoteItemRow])
	if err != nil {
		return err
	}

	byQuote := make(map[string][]QuoteItem, len(list))
	for _, it := range itemRows {
		byQuote[it.QuoteID] = append(byQuote[it.QuoteID], QuoteItem{
			ID: it.ID, ProductID: it.ProductID, Name: it.Name, SKU: it.SKU,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice, TaxClass: it.TaxClass,
		})
	}
	for i := range list {
		list[i].Items = byQuote[list[i].ID]
		if list[i].Items == nil {
			list[i].Items = []QuoteItem{}
		}
	}
	return nil
}

var validStatuses = map[string]bool{"draft": true, "sent": true, "accepted": true, "expired": true}

// UpdateQuoteStatus never allows moving into or out of "converted" — that
// transition only happens atomically inside ConvertToOrder.
func (r *Repo) UpdateQuoteStatus(ctx context.Context, id, status string) error {
	if !validStatuses[status] {
		return fmt.Errorf("invalid status %q", status)
	}
	ct, err := r.pool.Exec(ctx, `
		UPDATE quotes SET status = $2, updated_at = now()
		WHERE id = $1 AND status != 'converted'`, id, status)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ConvertToOrder only accepts quotes in "sent" or "accepted" status — a
// draft hasn't been shown to the customer yet, and a converted/expired one
// is terminal. Order fields deliberately chosen: delivery_method="pickup"
// (B2B quote customers typically collect or arrange their own freight,
// negotiated outside the normal checkout flow — shippingFeeFor already
// zero-rates pickup) and payment_method="invoice" (an unconstrained text
// column already, matching orders.payment_terms' existing "Net 30"-style
// concept for B2B buyers).
func (r *Repo) ConvertToOrder(ctx context.Context, quoteID string) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var status string
	var customerName, customerEmail, customerPhone *string
	var totalAmount float64
	if err := tx.QueryRow(ctx, `
		SELECT status, customer_name, customer_email, customer_phone, total_amount
		FROM quotes WHERE id = $1 FOR UPDATE`, quoteID).
		Scan(&status, &customerName, &customerEmail, &customerPhone, &totalAmount); err != nil {
		return "", fmt.Errorf("load quote: %w", err)
	}
	if status != "sent" && status != "accepted" {
		return "", fmt.Errorf("only sent or accepted quotes can be converted (current status: %s)", status)
	}

	rows, err := tx.Query(ctx, `
		SELECT product_id::text, name, quantity, unit_price, tax_class FROM quote_items WHERE quote_id = $1`, quoteID)
	if err != nil {
		return "", err
	}
	type item struct {
		ProductID *string
		Name      string
		Quantity  int32
		UnitPrice float64
		TaxClass  string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ProductID, &it.Name, &it.Quantity, &it.UnitPrice, &it.TaxClass); err != nil {
			rows.Close()
			return "", err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", fmt.Errorf("quote has no items")
	}

	// Best-effort current cost_price per line for margin reporting — 0 for
	// any line with no linked product (a custom/free-text quote line).
	costPrices := make(map[string]float64, len(items))
	var totalCost float64
	for _, it := range items {
		if it.ProductID == nil {
			continue
		}
		var cost float64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(cost_price, 0) FROM products WHERE id = $1`, *it.ProductID).Scan(&cost); err == nil {
			costPrices[*it.ProductID] = cost
			totalCost += cost * float64(it.Quantity)
		}
	}
	totalProfit := totalAmount - totalCost

	shippingAddr, err := json.Marshal(map[string]string{
		"first_name": derefStr(customerName),
		"email":      derefStr(customerEmail),
		"phone":      derefStr(customerPhone),
	})
	if err != nil {
		return "", err
	}

	var orderID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO orders (total_amount, shipping_fee, delivery_method, total_cost, total_profit,
			status, payment_method, payment_status, shipping_address)
		VALUES ($1, 0, 'pickup', $2, $3, 'PENDING', 'invoice', 'unpaid', $4)
		RETURNING id::text`, totalAmount, totalCost, totalProfit, shippingAddr,
	).Scan(&orderID); err != nil {
		return "", fmt.Errorf("insert order: %w", err)
	}

	for _, it := range items {
		var cost float64
		if it.ProductID != nil {
			cost = costPrices[*it.ProductID]
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items (order_id, product_id, quantity, unit_price, cost_price, tax_class)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			orderID, it.ProductID, it.Quantity, it.UnitPrice, cost, it.TaxClass); err != nil {
			return "", fmt.Errorf("insert order item: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE quotes SET status = 'converted', converted_order_id = $2, updated_at = now() WHERE id = $1`,
		quoteID, orderID); err != nil {
		return "", fmt.Errorf("mark quote converted: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return orderID, nil
}
