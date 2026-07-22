package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// OrderReadyForPickupArgs is enqueued by the admin order-update handler
// when an order transitions to READY_FOR_PICKUP — the click-and-collect
// counterpart of OrderShippedNotificationArgs.
type OrderReadyForPickupArgs struct {
	OrderID string `json:"order_id"`
}

func (OrderReadyForPickupArgs) Kind() string { return "order_ready_for_pickup" }

type OrderReadyForPickupWorker struct {
	river.WorkerDefaults[OrderReadyForPickupArgs]
	Pool    *pgxpool.Pool
	Email   *EmailSender
	SiteURL string
}

func (w *OrderReadyForPickupWorker) Work(ctx context.Context, job *river.Job[OrderReadyForPickupArgs]) error {
	var totalAmount float64
	var paymentStatus *string
	var shippingRaw []byte

	err := w.Pool.QueryRow(ctx, `SELECT total_amount::float8, payment_status, shipping_address FROM orders WHERE id = $1`, job.Args.OrderID).
		Scan(&totalAmount, &paymentStatus, &shippingRaw)
	if err != nil {
		return fmt.Errorf("load order %s: %w", job.Args.OrderID, err)
	}

	var sa shippingAddress
	if len(shippingRaw) > 0 && string(shippingRaw) != "null" {
		_ = json.Unmarshal(shippingRaw, &sa)
	}
	buyerEmail := strings.TrimSpace(sa.Email)
	if buyerEmail == "" {
		fmt.Printf("order-ready-for-pickup: no customer email for order %s, skip\n", job.Args.OrderID)
		return nil
	}

	orderIDShort := strings.ToUpper(job.Args.OrderID[:8])
	payLine := ""
	if paymentStatus == nil || strings.ToLower(*paymentStatus) != "paid" {
		payLine = fmt.Sprintf(`<p>Amount due on collection: <strong>KES %s</strong> (cash or M-Pesa at the counter).</p>`, fmtMoney(totalAmount))
	}

	html := fmt.Sprintf(`
		<h1>Your order is ready for pickup</h1>
		<p>Order <strong>#%s</strong> is packed and waiting for you at our store.</p>
		%s
		<p>Please bring this email or your order number when collecting.</p>
		<p style="color:#64748b;font-size:14px">Thank you for shopping with GizmoJunction.</p>
	`, orderIDShort, payLine)

	return w.Email.Send(ctx, EmailPayload{
		From:    "Gizmo Junction <noreply@notify.gizmojunction.com>",
		To:      []string{buyerEmail},
		Subject: fmt.Sprintf("Order #%s is ready for pickup", orderIDShort),
		HTML:    html,
	})
}
