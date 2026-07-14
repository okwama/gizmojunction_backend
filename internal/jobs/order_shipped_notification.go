package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// OrderShippedNotificationArgs ports order-shipped-notification. The
// original checked old_record.status != 'SHIPPED' to avoid re-sending on
// every subsequent update — that "is this newly shipped" decision belongs
// to whatever updates the order status (Phase 5), not this job; by the
// time this job is enqueued, the caller has already decided it's a new
// shipment.
type OrderShippedNotificationArgs struct {
	OrderID string `json:"order_id"`
}

func (OrderShippedNotificationArgs) Kind() string { return "order_shipped_notification" }

type OrderShippedNotificationWorker struct {
	river.WorkerDefaults[OrderShippedNotificationArgs]
	Pool    *pgxpool.Pool
	Email   *EmailSender
	SiteURL string
}

func (w *OrderShippedNotificationWorker) Work(ctx context.Context, job *river.Job[OrderShippedNotificationArgs]) error {
	var totalAmount float64
	var shippingRaw []byte

	err := w.Pool.QueryRow(ctx, `SELECT total_amount::float8, shipping_address FROM orders WHERE id = $1`, job.Args.OrderID).
		Scan(&totalAmount, &shippingRaw)
	if err != nil {
		return fmt.Errorf("load order %s: %w", job.Args.OrderID, err)
	}

	var sa shippingAddress
	if len(shippingRaw) > 0 && string(shippingRaw) != "null" {
		_ = json.Unmarshal(shippingRaw, &sa)
	}
	buyerEmail := strings.TrimSpace(sa.Email)
	if buyerEmail == "" {
		fmt.Printf("order-shipped-notification: no customer email for order %s, skip\n", job.Args.OrderID)
		return nil
	}

	orderIDShort := strings.ToUpper(job.Args.OrderID[:8])
	trackURL := fmt.Sprintf("%s/track-order?id=%s&email=%s", strings.TrimRight(w.SiteURL, "/"), job.Args.OrderID, buyerEmail)

	html := fmt.Sprintf(`
		<h1>Your order is on the way</h1>
		<p>Good news — order <strong>#%s</strong> (KES %s) has been <strong>shipped</strong>.</p>
		<p>Track delivery and download your receipt when ready:</p>
		<p><a href="%s" style="display:inline-block;padding:12px 20px;background:#002d72;color:#fff;text-decoration:none;border-radius:6px;font-weight:bold">Track your order</a></p>
		<p style="color:#64748b;font-size:14px">Thank you for shopping with GizmoJunction.</p>
	`, orderIDShort, fmtMoney(totalAmount), trackURL)

	return w.Email.Send(ctx, EmailPayload{
		From:    "Gizmo Junction <noreply@gizmojunction.com>",
		To:      []string{buyerEmail},
		Subject: fmt.Sprintf("Your GizmoJunction order #%s has shipped", orderIDShort),
		HTML:    html,
	})
}
