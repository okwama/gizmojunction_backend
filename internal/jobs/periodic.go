package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const lowStockThreshold = 5

// StockAlertsArgs ports supabase/functions/stock-alerts. The original never
// actually sent an email either ("In a real scenario, we would trigger an
// email... just console.log for now") — this preserves that exact stub
// behavior rather than silently turning on a notification that never
// fired before.
type StockAlertsArgs struct{}

func (StockAlertsArgs) Kind() string { return "stock_alerts" }

type StockAlertsWorker struct {
	river.WorkerDefaults[StockAlertsArgs]
	Pool *pgxpool.Pool
}

type lowStockItem struct {
	Name     string `db:"name"`
	SKU      string `db:"sku"`
	StockQty int32  `db:"stock_quantity"`
}

func (w *StockAlertsWorker) Work(ctx context.Context, _ *river.Job[StockAlertsArgs]) error {
	rows, err := w.Pool.Query(ctx, `
		SELECT name, sku, stock_quantity FROM products
		WHERE stock_quantity < $1 AND is_published = true
	`, lowStockThreshold)
	if err != nil {
		return fmt.Errorf("query low stock products: %w", err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[lowStockItem])
	if err != nil {
		return fmt.Errorf("scan low stock products: %w", err)
	}

	if len(items) > 0 {
		log.Printf("Alert: %d items are low on stock!", len(items))
		for _, item := range items {
			log.Printf("LOW STOCK: %s (%s) - Only %d remaining.", item.Name, item.SKU, item.StockQty)
		}
	}
	return nil
}

// DailySalesSnapshotArgs ports daily-sales-snapshot — also a log-only stub
// in the original (no table it persists to).
type DailySalesSnapshotArgs struct{}

func (DailySalesSnapshotArgs) Kind() string { return "daily_sales_snapshot" }

type DailySalesSnapshotWorker struct {
	river.WorkerDefaults[DailySalesSnapshotArgs]
	Pool *pgxpool.Pool
}

type dailyOrder struct {
	TotalAmount float64 `db:"total_amount"`
	Status      string  `db:"status"`
}

type dailyOrderItem struct {
	Quantity    int32  `db:"quantity"`
	ProductName string `db:"product_name"`
}

func (w *DailySalesSnapshotWorker) Work(ctx context.Context, _ *river.Job[DailySalesSnapshotArgs]) error {
	since := time.Now().Add(-24 * time.Hour)

	orderRows, err := w.Pool.Query(ctx, `SELECT total_amount::float8, status::text FROM orders WHERE created_at > $1`, since)
	if err != nil {
		return fmt.Errorf("query orders: %w", err)
	}
	orders, err := pgx.CollectRows(orderRows, pgx.RowToStructByName[dailyOrder])
	if err != nil {
		return fmt.Errorf("scan orders: %w", err)
	}

	var totalRevenue float64
	paidOrders := 0
	for _, o := range orders {
		totalRevenue += o.TotalAmount
		if o.Status != "PENDING" && o.Status != "CANCELLED" {
			paidOrders++
		}
	}

	itemRows, err := w.Pool.Query(ctx, `
		SELECT oi.quantity, p.name AS product_name
		FROM order_items oi
		JOIN products p ON p.id = oi.product_id
		JOIN orders o ON o.id = oi.order_id
		WHERE o.created_at > $1
	`, since)
	if err != nil {
		return fmt.Errorf("query order items: %w", err)
	}
	items, err := pgx.CollectRows(itemRows, pgx.RowToStructByName[dailyOrderItem])
	if err != nil {
		return fmt.Errorf("scan order items: %w", err)
	}

	productStats := map[string]int32{}
	for _, it := range items {
		productStats[it.ProductName] += it.Quantity
	}
	type topSeller struct {
		Name string `json:"name"`
		Qty  int32  `json:"qty"`
	}
	var topSellers []topSeller
	for name, qty := range productStats {
		topSellers = append(topSellers, topSeller{Name: name, Qty: qty})
	}
	sort.Slice(topSellers, func(i, j int) bool { return topSellers[i].Qty > topSellers[j].Qty })
	if len(topSellers) > 3 {
		topSellers = topSellers[:3]
	}

	summary := map[string]any{
		"date":          time.Now().Format("2006-01-02"),
		"total_orders":  len(orders),
		"paid_orders":   paidOrders,
		"total_revenue": totalRevenue,
		"top_sellers":   topSellers,
	}
	summaryJSON, _ := json.MarshalIndent(summary, "", "  ")
	log.Printf("Daily Sales Snapshot: %s", summaryJSON)
	return nil
}

// AbandonedCartRecoveryArgs ports abandoned-cart-recovery — the original
// only ever logged a "would send" message ("Simulate sending recovery
// email"), never an actual send, so this preserves that. It also fixes a
// real N+1 in the original: a per-order supabase.auth.admin.getUserById
// call became a single joined query against profiles.email.
type AbandonedCartRecoveryArgs struct{}

func (AbandonedCartRecoveryArgs) Kind() string { return "abandoned_cart_recovery" }

type AbandonedCartRecoveryWorker struct {
	river.WorkerDefaults[AbandonedCartRecoveryArgs]
	Pool *pgxpool.Pool
}

type abandonedCart struct {
	OrderID     string  `db:"order_id"`
	TotalAmount float64 `db:"total_amount"`
	Email       *string `db:"email"`
	Items       []byte  `db:"items"`
}

func (w *AbandonedCartRecoveryWorker) Work(ctx context.Context, _ *river.Job[AbandonedCartRecoveryArgs]) error {
	now := time.Now()
	twoHoursAgo := now.Add(-2 * time.Hour)
	fourHoursAgo := now.Add(-4 * time.Hour)

	rows, err := w.Pool.Query(ctx, `
		SELECT o.id AS order_id, o.total_amount::float8, p.email,
			COALESCE(
				(SELECT json_agg(json_build_object('name', pr.name, 'quantity', oi.quantity))
				 FROM order_items oi JOIN products pr ON pr.id = oi.product_id
				 WHERE oi.order_id = o.id),
				'[]'
			) AS items
		FROM orders o
		LEFT JOIN profiles p ON p.id = o.customer_id
		WHERE o.status = 'PENDING' AND o.created_at < $1 AND o.created_at > $2
	`, twoHoursAgo, fourHoursAgo)
	if err != nil {
		return fmt.Errorf("query abandoned orders: %w", err)
	}
	carts, err := pgx.CollectRows(rows, pgx.RowToStructByName[abandonedCart])
	if err != nil {
		return fmt.Errorf("scan abandoned orders: %w", err)
	}

	if len(carts) == 0 {
		return nil
	}
	log.Printf("Found %d abandoned carts. Processing...", len(carts))

	for _, cart := range carts {
		if cart.Email == nil || *cart.Email == "" {
			log.Printf("Could not resolve email for order %s, skipping", cart.OrderID)
			continue
		}
		var items []struct {
			Name     string `json:"name"`
			Quantity int32  `json:"quantity"`
		}
		_ = json.Unmarshal(cart.Items, &items)

		itemsList := ""
		for _, it := range items {
			itemsList += fmt.Sprintf("- %s (x%d)\n", it.Name, it.Quantity)
		}

		log.Printf("[ABANDONED CART RECOVERY]\nTo: %s\nSubject: Did you forget something?\nMessage: You left these items in your cart at GizmoJunction:\n%sTotal: %.2f\nComplete your order here: https://gizmojunction.com/checkout?id=%s",
			*cart.Email, itemsList, cart.TotalAmount, cart.OrderID)
	}

	return nil
}
