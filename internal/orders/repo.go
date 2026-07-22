// Package orders owns the orders domain. Since the July 2026 cutover both
// pools point at Neon; the db/catalog split is kept only because the queries
// are already written against it.
package orders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repo struct {
	db      *pgxpool.Pool // Supabase: orders, order_items, tax_invoices
	catalog *pgxpool.Pool // Neon: products, profiles
}

func NewRepo(db, catalog *pgxpool.Pool) *Repo {
	return &Repo{db: db, catalog: catalog}
}

type ProductInfo struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	SKU      *string `json:"sku,omitempty"`
	ImageURL *string `json:"image_url,omitempty"`
	Price    float64 `json:"price"`
}

type OrderItem struct {
	ID        string       `db:"id" json:"id"`
	ProductID *string      `db:"product_id" json:"product_id,omitempty"`
	Quantity  int32        `db:"quantity" json:"quantity"`
	UnitPrice float64      `db:"unit_price" json:"unit_price"`
	CostPrice float64      `db:"cost_price" json:"cost_price"`
	Product   *ProductInfo `db:"-" json:"product,omitempty"`
}

type TaxInvoiceInfo struct {
	Status          *string `json:"status,omitempty"`
	ReceiptPDFPath  *string `json:"receipt_pdf_path,omitempty"`
	CUInvoiceNumber *string `json:"cu_invoice_number,omitempty"`
}

type Order struct {
	ID              string          `db:"id" json:"id"`
	CustomerID      *string         `db:"customer_id" json:"customer_id,omitempty"`
	Status          string          `db:"status" json:"status"`
	TotalAmount     float64         `db:"total_amount" json:"total_amount"`
	ShippingFee     float64         `db:"shipping_fee" json:"shipping_fee"`
	DeliveryMethod  *string         `db:"delivery_method" json:"delivery_method,omitempty"`
	ShippingAddress json.RawMessage `db:"shipping_address" json:"shipping_address,omitempty"`
	PaymentMethod   *string         `db:"payment_method" json:"payment_method,omitempty"`
	PaymentStatus   *string         `db:"payment_status" json:"payment_status,omitempty"`
	TotalCost       float64         `db:"total_cost" json:"total_cost"`
	TotalProfit     float64         `db:"total_profit" json:"total_profit"`
	TaxStatus       *string         `db:"tax_status" json:"tax_status,omitempty"`
	TaxInvoiceID    *string         `db:"tax_invoice_id" json:"tax_invoice_id,omitempty"`
	KraPin          *string         `db:"kra_pin" json:"kra_pin,omitempty"`
	LoyaltyEnrolled bool            `db:"loyalty_enrolled" json:"loyalty_enrolled"`
	CreatedAt       time.Time       `db:"created_at" json:"created_at"`
	Items           []OrderItem     `db:"-" json:"order_items,omitempty"`
	TaxInvoice      *TaxInvoiceInfo `db:"-" json:"tax_invoice,omitempty"`
}

const orderColumns = `id::text, customer_id::text, COALESCE(status::text, 'PENDING') AS status,
	total_amount::float8, COALESCE(shipping_fee, 0)::float8 AS shipping_fee, delivery_method,
	shipping_address, payment_method, payment_status,
	COALESCE(total_cost, 0)::float8 AS total_cost, COALESCE(total_profit, 0)::float8 AS total_profit,
	tax_status::text, tax_invoice_id::text, kra_pin, COALESCE(loyalty_enrolled, false) AS loyalty_enrolled, created_at`

// --- Creation (checkout) ---

type NewOrderItem struct {
	ProductID string `json:"product_id"`
	Quantity  int32  `json:"quantity" minimum:"1"`
	// UnitPrice is accepted for API compatibility but ignored — prices are
	// snapshotted server-side from the catalog.
	UnitPrice float64 `json:"unit_price,omitempty" required:"false"`
}

type NewOrder struct {
	Items []NewOrderItem `json:"items"`
	// TotalAmount and ShippingFee are still accepted for API compatibility
	// but ignored — both are recomputed server-side from the catalog and
	// shipping_zones so a tampered request can't set its own prices.
	TotalAmount     float64         `json:"total_amount,omitempty" required:"false"`
	ShippingFee     float64         `json:"shipping_fee,omitempty" required:"false"`
	DeliveryMethod  string          `json:"delivery_method,omitempty"`
	PaymentMethod   string          `json:"payment_method"`
	ShippingAddress json.RawMessage `json:"shipping_address"`
	KraPin          string          `json:"kra_pin,omitempty"`
	LoyaltyEnrolled bool            `json:"loyalty_enrolled,omitempty"`
}

// freeDeliveryThreshold mirrors FREE_DELIVERY_THRESHOLD in the checkout
// page — orders at or above it ship free.
const freeDeliveryThreshold = 5000

// ErrUnavailable marks checkout rejections the customer can act on
// (unknown/unpublished product); handlers map it to a 400, not a 500.
var ErrUnavailable = errors.New("product unavailable")

// shippingFeeFor recomputes the delivery fee server-side: pickup is always
// free, orders over the threshold are free, otherwise the county's zone
// rate applies. Unknown county falls back to 0 (matching the checkout UI,
// which shows "Rate unavailable" but still allows the order).
func (r *Repo) shippingFeeFor(ctx context.Context, deliveryMethod, county string, subtotal float64) (float64, error) {
	if deliveryMethod == "pickup" || subtotal >= freeDeliveryThreshold || county == "" {
		return 0, nil
	}
	col := "standard_fee"
	if deliveryMethod == "express" {
		col = "express_fee"
	}
	var fee float64
	err := r.db.QueryRow(ctx, `
		SELECT `+col+`::float8 FROM shipping_zones
		WHERE is_active = true AND $1 = ANY(counties)
		ORDER BY sort_order LIMIT 1`, county).Scan(&fee)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return fee, err
}

// CreateOrder prices the order entirely server-side: unit prices (sale
// price when set, list price otherwise), cost prices, and the shipping fee
// all come from the database, never the request — the client's
// unit_price/total_amount are ignored, so a tampered checkout request
// can't buy at its own price. Order + items are written in one
// transaction; COD orders also deduct stock immediately (online payments
// deduct when the payment webhook confirms).
func (r *Repo) CreateOrder(ctx context.Context, customerID *string, o NewOrder) (string, error) {
	method := o.DeliveryMethod
	switch method {
	case "", "standard":
		method = "standard"
	case "express", "pickup":
	default:
		return "", fmt.Errorf("%w: unknown delivery method %q", ErrUnavailable, o.DeliveryMethod)
	}

	ids := make([]string, 0, len(o.Items))
	for _, item := range o.Items {
		if item.Quantity <= 0 {
			return "", fmt.Errorf("%w: invalid quantity", ErrUnavailable)
		}
		ids = append(ids, item.ProductID)
	}

	type productRow struct {
		price, cost float64
	}
	products := map[string]productRow{}
	rows, err := r.catalog.Query(ctx, `
		SELECT id::text,
			CASE WHEN sale_price IS NOT NULL AND sale_price > 0 THEN sale_price ELSE price END::float8,
			COALESCE(cost_price, 0)::float8
		FROM products WHERE id = ANY($1::uuid[]) AND is_published = true`, ids)
	if err != nil {
		return "", fmt.Errorf("price lookup: %w", err)
	}
	for rows.Next() {
		var id string
		var p productRow
		if err := rows.Scan(&id, &p.price, &p.cost); err != nil {
			rows.Close()
			return "", err
		}
		products[id] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}

	var totalCost, subtotal float64
	for _, item := range o.Items {
		p, ok := products[item.ProductID]
		if !ok {
			return "", fmt.Errorf("%w: product %s is no longer available", ErrUnavailable, item.ProductID)
		}
		totalCost += p.cost * float64(item.Quantity)
		subtotal += p.price * float64(item.Quantity)
	}
	totalProfit := subtotal - totalCost

	county := ""
	if len(o.ShippingAddress) > 0 {
		var sa struct {
			County string `json:"county"`
		}
		_ = json.Unmarshal(o.ShippingAddress, &sa)
		county = sa.County
	}
	shippingFee, err := r.shippingFeeFor(ctx, method, county, subtotal)
	if err != nil {
		return "", fmt.Errorf("shipping fee lookup: %w", err)
	}
	totalAmount := subtotal + shippingFee

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var orderID string
	err = tx.QueryRow(ctx, `
		INSERT INTO orders (customer_id, total_amount, shipping_fee, delivery_method, total_cost, total_profit,
			status, payment_method, payment_status, shipping_address, kra_pin, loyalty_enrolled)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, 'PENDING', $7, 'unpaid', $8, NULLIF($9, ''), $10)
		RETURNING id::text`,
		customerID, totalAmount, shippingFee, method, totalCost, totalProfit,
		o.PaymentMethod, o.ShippingAddress, o.KraPin, o.LoyaltyEnrolled).Scan(&orderID)
	if err != nil {
		return "", err
	}

	for _, item := range o.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items (order_id, product_id, quantity, unit_price, cost_price)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5)`,
			orderID, item.ProductID, item.Quantity, products[item.ProductID].price, products[item.ProductID].cost); err != nil {
			return "", err
		}
	}

	// COD/pay-on-collection orders go straight to fulfilment, so their
	// stock leaves the shelf now; online payments deduct on the paid
	// webhook instead (see DecrementStock).
	if o.PaymentMethod == "cod" {
		if err := decrementStockTx(ctx, tx, orderID); err != nil {
			return "", err
		}
	}

	return orderID, tx.Commit(ctx)
}

// decrementStockTx deducts an order's quantities from product stock and
// flags the order, inside the caller's transaction. The stock_decremented
// guard makes it idempotent — a second call for the same order is a no-op.
func decrementStockTx(ctx context.Context, tx pgx.Tx, orderID string) error {
	var already bool
	if err := tx.QueryRow(ctx, `
		SELECT stock_decremented FROM orders WHERE id = $1 FOR UPDATE`, orderID).Scan(&already); err != nil {
		return fmt.Errorf("stock flag lookup: %w", err)
	}
	if already {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE products p SET stock_quantity = GREATEST(p.stock_quantity - oi.quantity, 0)
		FROM order_items oi
		WHERE oi.order_id = $1 AND p.id = oi.product_id`, orderID); err != nil {
		return fmt.Errorf("stock decrement: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET stock_decremented = true WHERE id = $1`, orderID); err != nil {
		return fmt.Errorf("stock flag update: %w", err)
	}
	return nil
}

// DecrementStock is the standalone form used by the payment webhooks once
// an order transitions to paid.
func (r *Repo) DecrementStock(ctx context.Context, orderID string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := decrementStockTx(ctx, tx, orderID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RestoreStock reverses a previous decrement when an order is cancelled.
// Only orders whose stock was actually deducted are restored.
func (r *Repo) RestoreStock(ctx context.Context, orderID string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var decremented bool
	if err := tx.QueryRow(ctx, `
		SELECT stock_decremented FROM orders WHERE id = $1 FOR UPDATE`, orderID).Scan(&decremented); err != nil {
		return fmt.Errorf("stock flag lookup: %w", err)
	}
	if !decremented {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE products p SET stock_quantity = p.stock_quantity + oi.quantity
		FROM order_items oi
		WHERE oi.order_id = $1 AND p.id = oi.product_id`, orderID); err != nil {
		return fmt.Errorf("stock restore: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET stock_decremented = false WHERE id = $1`, orderID); err != nil {
		return fmt.Errorf("stock flag update: %w", err)
	}
	return tx.Commit(ctx)
}

// --- Reads ---

func (r *Repo) itemsFor(ctx context.Context, orderIDs []string) (map[string][]OrderItem, error) {
	rows, err := r.db.Query(ctx, `
		SELECT order_id::text, id::text, product_id::text, quantity, unit_price::float8, COALESCE(cost_price, 0)::float8
		FROM order_items WHERE order_id = ANY($1::uuid[])`, orderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byOrder := map[string][]OrderItem{}
	productIDs := map[string]bool{}
	for rows.Next() {
		var orderID string
		var item OrderItem
		if err := rows.Scan(&orderID, &item.ID, &item.ProductID, &item.Quantity, &item.UnitPrice, &item.CostPrice); err != nil {
			return nil, err
		}
		if item.ProductID != nil {
			productIDs[*item.ProductID] = true
		}
		byOrder[orderID] = append(byOrder[orderID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	products, err := r.productInfo(ctx, keys(productIDs))
	if err != nil {
		return nil, err
	}
	for orderID, items := range byOrder {
		for i := range items {
			if items[i].ProductID != nil {
				if p, ok := products[*items[i].ProductID]; ok {
					items[i].Product = p
				}
			}
		}
		byOrder[orderID] = items
	}
	return byOrder, nil
}

func (r *Repo) productInfo(ctx context.Context, ids []string) (map[string]*ProductInfo, error) {
	out := map[string]*ProductInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.catalog.Query(ctx, `
		SELECT id::text, name, sku, image_url, price::float8 FROM products WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p ProductInfo
		if err := rows.Scan(&p.ID, &p.Name, &p.SKU, &p.ImageURL, &p.Price); err != nil {
			return nil, err
		}
		out[p.ID] = &p
	}
	return out, rows.Err()
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (r *Repo) attachItems(ctx context.Context, orders []Order) error {
	if len(orders) == 0 {
		return nil
	}
	ids := make([]string, 0, len(orders))
	for _, o := range orders {
		ids = append(ids, o.ID)
	}
	items, err := r.itemsFor(ctx, ids)
	if err != nil {
		return err
	}
	for i := range orders {
		orders[i].Items = items[orders[i].ID]
		if orders[i].Items == nil {
			orders[i].Items = []OrderItem{}
		}
	}
	return nil
}

func (r *Repo) OrderByID(ctx context.Context, id string) (*Order, error) {
	rows, err := r.db.Query(ctx, `SELECT `+orderColumns+` FROM orders WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	order, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Order])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	list := []Order{order}
	if err := r.attachItems(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

// TrackOrder mirrors the old browser logic (all orders for the email, then
// prefix-match the id) in SQL, preserving the two distinct error messages.
func (r *Repo) TrackOrder(ctx context.Context, email, orderID string) (*Order, string, error) {
	var emailCount int
	if err := r.db.QueryRow(ctx, `
		SELECT count(*) FROM orders WHERE lower(shipping_address->>'email') = lower($1)`, email).Scan(&emailCount); err != nil {
		return nil, "", err
	}
	if emailCount == 0 {
		return nil, "email", nil
	}

	rows, err := r.db.Query(ctx, `
		SELECT `+orderColumns+` FROM orders
		WHERE lower(shipping_address->>'email') = lower($1) AND id::text ILIKE $2 || '%'
		ORDER BY created_at DESC LIMIT 1`, email, orderID)
	if err != nil {
		return nil, "", err
	}
	order, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Order])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "id", nil
	}
	if err != nil {
		return nil, "", err
	}
	list := []Order{order}
	if err := r.attachItems(ctx, list); err != nil {
		return nil, "", err
	}

	// The tracking page offers an invoice download — include the receipt
	// path so it doesn't need a second lookup.
	var receiptPath *string
	if err := r.db.QueryRow(ctx, `SELECT receipt_pdf_path FROM tax_invoices WHERE order_id = $1`, list[0].ID).Scan(&receiptPath); err == nil && receiptPath != nil {
		list[0].TaxInvoice = &TaxInvoiceInfo{ReceiptPDFPath: receiptPath}
	}
	return &list[0], "", nil
}

// CheckoutSummary calls the get_checkout_order_summary SQL function that
// already lives in the orders database (previously invoked as a Supabase
// RPC from the browser).
func (r *Repo) CheckoutSummary(ctx context.Context, orderID, email string) (json.RawMessage, error) {
	var summary json.RawMessage
	err := r.db.QueryRow(ctx, `SELECT public.get_checkout_order_summary($1::uuid, $2)`, orderID, email).Scan(&summary)
	return summary, err
}

func (r *Repo) AdminList(ctx context.Context) ([]Order, error) {
	rows, err := r.db.Query(ctx, `SELECT `+orderColumns+` FROM orders ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	orders, err := pgx.CollectRows(rows, pgx.RowToStructByName[Order])
	if err != nil {
		return nil, err
	}
	if err := r.attachItems(ctx, orders); err != nil {
		return nil, err
	}
	return orders, nil
}

// UpdateOrder returns the order's previous status so the handler can tell
// a real transition (fire shipped/pickup emails, restore stock on
// cancellation) from a repeat save of the same status.
func (r *Repo) UpdateOrder(ctx context.Context, id string, status, kraPin *string) (string, error) {
	var oldStatus string
	err := r.db.QueryRow(ctx, `
		UPDATE orders o SET
			status = COALESCE($2::order_status, o.status),
			kra_pin = COALESCE($3, o.kra_pin)
		FROM (SELECT id, COALESCE(status::text, 'PENDING') AS status FROM orders WHERE id = $1 FOR UPDATE) prev
		WHERE o.id = prev.id
		RETURNING prev.status`, id, status, kraPin).Scan(&oldStatus)
	return oldStatus, err
}

func (r *Repo) PaidOrders(ctx context.Context) ([]Order, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+orderColumns+` FROM orders WHERE payment_status = 'paid' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Order])
}

// TaxOrders feeds the admin Tax page: orders plus their tax invoice's
// status/receipt/CUIN (previously a supabase nested select).
func (r *Repo) TaxOrders(ctx context.Context) ([]Order, error) {
	rows, err := r.db.Query(ctx, `
		SELECT o.id::text, o.customer_id::text, COALESCE(o.status::text, 'PENDING'), o.total_amount::float8,
			COALESCE(o.shipping_fee, 0)::float8, o.delivery_method, o.shipping_address, o.payment_method,
			o.payment_status, COALESCE(o.total_cost, 0)::float8, COALESCE(o.total_profit, 0)::float8,
			o.tax_status::text, o.tax_invoice_id::text, o.kra_pin, COALESCE(o.loyalty_enrolled, false), o.created_at,
			ti.status::text, ti.receipt_pdf_path, ti.cu_invoice_number
		FROM orders o
		LEFT JOIN tax_invoices ti ON ti.order_id = o.id
		ORDER BY o.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []Order
	for rows.Next() {
		var o Order
		var tiStatus, tiPath, tiCUIN *string
		if err := rows.Scan(&o.ID, &o.CustomerID, &o.Status, &o.TotalAmount, &o.ShippingFee, &o.DeliveryMethod,
			&o.ShippingAddress, &o.PaymentMethod, &o.PaymentStatus, &o.TotalCost, &o.TotalProfit,
			&o.TaxStatus, &o.TaxInvoiceID, &o.KraPin, &o.LoyaltyEnrolled, &o.CreatedAt,
			&tiStatus, &tiPath, &tiCUIN); err != nil {
			return nil, err
		}
		if tiStatus != nil || tiPath != nil || tiCUIN != nil {
			o.TaxInvoice = &TaxInvoiceInfo{Status: tiStatus, ReceiptPDFPath: tiPath, CUInvoiceNumber: tiCUIN}
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

// --- Reports & dashboard aggregates ---

type ReportItem struct {
	OrderID      string  `json:"order_id"`
	Quantity     int32   `json:"quantity"`
	UnitPrice    float64 `json:"unit_price"`
	ProductName  *string `json:"product_name,omitempty"`
	CategoryName *string `json:"category_name,omitempty"`
}

type ReportsData struct {
	PaidOrders     []Order      `json:"paid_orders"`
	PaidOrderItems []ReportItem `json:"paid_order_items"`
	InventoryValue float64      `json:"inventory_value"`
	InventoryCount int          `json:"inventory_count"`
}

func (r *Repo) Reports(ctx context.Context) (ReportsData, error) {
	var data ReportsData

	paid, err := r.PaidOrders(ctx)
	if err != nil {
		return data, err
	}
	data.PaidOrders = paid
	if data.PaidOrders == nil {
		data.PaidOrders = []Order{}
	}

	orderIDs := make([]string, 0, len(paid))
	for _, o := range paid {
		orderIDs = append(orderIDs, o.ID)
	}
	data.PaidOrderItems = []ReportItem{}
	if len(orderIDs) > 0 {
		rows, err := r.db.Query(ctx, `
			SELECT order_id::text, product_id::text, quantity, unit_price::float8
			FROM order_items WHERE order_id = ANY($1::uuid[])`, orderIDs)
		if err != nil {
			return data, err
		}
		type rawItem struct {
			OrderID   string
			ProductID *string
			Quantity  int32
			UnitPrice float64
		}
		var raw []rawItem
		productIDs := map[string]bool{}
		for rows.Next() {
			var it rawItem
			if err := rows.Scan(&it.OrderID, &it.ProductID, &it.Quantity, &it.UnitPrice); err != nil {
				rows.Close()
				return data, err
			}
			if it.ProductID != nil {
				productIDs[*it.ProductID] = true
			}
			raw = append(raw, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return data, err
		}

		type prodMeta struct{ Name, Category *string }
		meta := map[string]prodMeta{}
		if len(productIDs) > 0 {
			prows, err := r.catalog.Query(ctx, `
				SELECT p.id::text, p.name, c.name FROM products p
				LEFT JOIN categories c ON c.id = p.category_id
				WHERE p.id = ANY($1::uuid[])`, keys(productIDs))
			if err != nil {
				return data, err
			}
			for prows.Next() {
				var id string
				var name, cat *string
				if err := prows.Scan(&id, &name, &cat); err != nil {
					prows.Close()
					return data, err
				}
				meta[id] = prodMeta{Name: name, Category: cat}
			}
			prows.Close()
		}
		for _, it := range raw {
			item := ReportItem{OrderID: it.OrderID, Quantity: it.Quantity, UnitPrice: it.UnitPrice}
			if it.ProductID != nil {
				if m, ok := meta[*it.ProductID]; ok {
					item.ProductName = m.Name
					item.CategoryName = m.Category
				}
			}
			data.PaidOrderItems = append(data.PaidOrderItems, item)
		}
	}

	err = r.catalog.QueryRow(ctx, `
		SELECT count(*), COALESCE(SUM(COALESCE(cost_price, 0) * COALESCE(stock_quantity, 0)), 0)::float8
		FROM products`).Scan(&data.InventoryCount, &data.InventoryValue)
	return data, err
}

type BestSeller struct {
	ProductID string  `json:"product_id"`
	Name      string  `json:"name"`
	ImageURL  *string `json:"image_url,omitempty"`
	Quantity  int64   `json:"quantity"`
}

type RevenuePoint struct {
	TotalAmount float64   `json:"total_amount"`
	CreatedAt   time.Time `json:"created_at"`
}

type DashboardData struct {
	TotalSales     float64        `json:"total_sales"`
	OrderCount     int            `json:"order_count"`
	CustomerCount  int            `json:"customer_count"`
	LowStockCount  int            `json:"low_stock_count"`
	RecentOrders   []Order        `json:"recent_orders"`
	BestSellers    []BestSeller   `json:"best_sellers"`
	RevenueHistory []RevenuePoint `json:"revenue_history"`
}

func (r *Repo) Dashboard(ctx context.Context) (DashboardData, error) {
	var data DashboardData

	if err := r.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(total_amount) FILTER (WHERE payment_status = 'paid'), 0)::float8, count(*)
		FROM orders`).Scan(&data.TotalSales, &data.OrderCount); err != nil {
		return data, err
	}
	if err := r.catalog.QueryRow(ctx, `SELECT count(*) FROM profiles WHERE role = 'CUSTOMER'`).Scan(&data.CustomerCount); err != nil {
		return data, err
	}
	if err := r.catalog.QueryRow(ctx, `SELECT count(*) FROM products WHERE stock_quantity < 10`).Scan(&data.LowStockCount); err != nil {
		return data, err
	}

	rows, err := r.db.Query(ctx, `SELECT `+orderColumns+` FROM orders ORDER BY created_at DESC LIMIT 5`)
	if err != nil {
		return data, err
	}
	if data.RecentOrders, err = pgx.CollectRows(rows, pgx.RowToStructByName[Order]); err != nil {
		return data, err
	}
	if data.RecentOrders == nil {
		data.RecentOrders = []Order{}
	}

	// Top sellers over the most recent 100 order lines, matching the old
	// dashboard's limit(100) sample.
	bsRows, err := r.db.Query(ctx, `
		SELECT product_id::text, SUM(quantity)::bigint AS qty
		FROM (SELECT product_id, quantity FROM order_items ORDER BY id DESC LIMIT 100) recent
		WHERE product_id IS NOT NULL
		GROUP BY product_id ORDER BY qty DESC LIMIT 5`)
	if err != nil {
		return data, err
	}
	type sellerRow struct {
		ProductID string
		Quantity  int64
	}
	var sellers []sellerRow
	sellerIDs := map[string]bool{}
	for bsRows.Next() {
		var s sellerRow
		if err := bsRows.Scan(&s.ProductID, &s.Quantity); err != nil {
			bsRows.Close()
			return data, err
		}
		sellerIDs[s.ProductID] = true
		sellers = append(sellers, s)
	}
	bsRows.Close()
	if err := bsRows.Err(); err != nil {
		return data, err
	}
	products, err := r.productInfo(ctx, keys(sellerIDs))
	if err != nil {
		return data, err
	}
	data.BestSellers = []BestSeller{}
	for _, s := range sellers {
		bs := BestSeller{ProductID: s.ProductID, Quantity: s.Quantity}
		if p, ok := products[s.ProductID]; ok {
			bs.Name = p.Name
			bs.ImageURL = p.ImageURL
		}
		data.BestSellers = append(data.BestSellers, bs)
	}

	rhRows, err := r.db.Query(ctx, `
		SELECT total_amount::float8, created_at FROM orders
		WHERE payment_status = 'paid' AND created_at >= now() - interval '30 days'
		ORDER BY created_at ASC`)
	if err != nil {
		return data, err
	}
	defer rhRows.Close()
	data.RevenueHistory = []RevenuePoint{}
	for rhRows.Next() {
		var p RevenuePoint
		if err := rhRows.Scan(&p.TotalAmount, &p.CreatedAt); err != nil {
			return data, err
		}
		data.RevenueHistory = append(data.RevenueHistory, p)
	}
	return data, rhRows.Err()
}
