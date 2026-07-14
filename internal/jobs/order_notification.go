package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

type shippingAddress struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Address   string `json:"address"`
	City      string `json:"city"`
	County    string `json:"county"`
	Phone     string `json:"phone"`
	Email     string `json:"email"`
}

type orderLine struct {
	Quantity  int32   `db:"quantity"`
	UnitPrice float64 `db:"unit_price"`
	Name      string  `db:"name"`
	ImageURL  *string `db:"image_url"`
}

type recommendedProduct struct {
	Name     string  `db:"name"`
	ImageURL *string `db:"image_url"`
	Price    float64 `db:"price"`
}

func fmtMoney(n float64) string {
	whole := int64(n)
	// simple thousands separator, matching the original's toLocaleString()
	s := strconv.FormatInt(whole, 10)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

func linesCardsHTML(lines []orderLine) string {
	var b strings.Builder
	for _, l := range lines {
		img := "https://gizmojunction.com/placeholder.png"
		if l.ImageURL != nil && *l.ImageURL != "" {
			img = *l.ImageURL
		}
		fmt.Fprintf(&b, `
			<div style="display:flex;gap:15px;margin-bottom:20px;padding-bottom:15px;border-bottom:1px solid #eee;">
				<img src="%s" alt="%s" style="width:80px;height:80px;object-fit:contain;background:#fff;border:1px solid #eee;border-radius:4px;" />
				<div style="flex:1;">
					<p style="margin:0;font-size:14px;font-weight:600;color:#007185;">%s</p>
					<p style="margin:4px 0;font-size:13px;color:#565959;">Quantity: %d</p>
					<p style="margin:0;font-size:14px;font-weight:700;color:#b12704;">KSH %s</p>
				</div>
			</div>`, img, escapeHTML(l.Name), escapeHTML(l.Name), l.Quantity, fmtMoney(l.UnitPrice))
	}
	return b.String()
}

func recsHTML(recs []recommendedProduct) string {
	var b strings.Builder
	for _, r := range recs {
		img := ""
		if r.ImageURL != nil {
			img = *r.ImageURL
		}
		fmt.Fprintf(&b, `
			<div style="display:inline-block;width:30%%;margin-right:3%%;text-align:center;vertical-align:top;">
				<img src="%s" style="width:100%%;height:100px;object-fit:contain;" />
				<p style="margin:0;font-size:11px;color:#007185;height:28px;overflow:hidden;">%s</p>
				<p style="margin:4px 0;font-size:12px;font-weight:700;color:#b12704;">KSH %s</p>
			</div>`, img, escapeHTML(r.Name), fmtMoney(r.Price))
	}
	return b.String()
}

func formatShippingHTML(sa *shippingAddress) string {
	if sa == nil {
		return `<p><em>Details in account</em></p>`
	}
	var parts []string
	if name := strings.TrimSpace(sa.FirstName + " " + sa.LastName); name != "" {
		parts = append(parts, name)
	}
	if sa.Address != "" {
		parts = append(parts, sa.Address)
	}
	if cityCounty := strings.Trim(strings.Join([]string{sa.City, sa.County}, ", "), ", "); cityCounty != "" {
		parts = append(parts, cityCounty)
	}
	if sa.Phone != "" {
		parts = append(parts, sa.Phone)
	}
	escaped := make([]string, len(parts))
	for i, p := range parts {
		escaped[i] = escapeHTML(p)
	}
	return `<p style="margin:0;line-height:1.6;font-size:14px;color:#111;">` + strings.Join(escaped, "<br/>") + `</p>`
}

// OrderNotificationArgs ports supabase/functions/order-notification: a
// buyer confirmation email plus an admin new-order alert. Nothing enqueues
// this yet — Phase 5 (order creation moving into this backend) is what will
// call client.Insert(ctx, OrderNotificationArgs{...}, nil) after placing an
// order. The worker itself is complete and independently testable now.
type OrderNotificationArgs struct {
	OrderID string `json:"order_id"`
}

func (OrderNotificationArgs) Kind() string { return "order_notification" }

type OrderNotificationWorker struct {
	river.WorkerDefaults[OrderNotificationArgs]
	Pool       *pgxpool.Pool
	Email      *EmailSender
	SiteURL    string
	AdminEmail string
}

func (w *OrderNotificationWorker) Work(ctx context.Context, job *river.Job[OrderNotificationArgs]) error {
	var totalAmount float64
	var paymentStatus, paymentMethod string
	var kraPin *string
	var shippingRaw []byte

	err := w.Pool.QueryRow(ctx, `
		SELECT total_amount::float8, payment_status, payment_method, shipping_address, kra_pin
		FROM orders WHERE id = $1
	`, job.Args.OrderID).Scan(&totalAmount, &paymentStatus, &paymentMethod, &shippingRaw, &kraPin)
	if err != nil {
		return fmt.Errorf("load order %s: %w", job.Args.OrderID, err)
	}

	var sa shippingAddress
	saPtr := &sa
	if len(shippingRaw) == 0 || string(shippingRaw) == "null" {
		saPtr = nil
	} else if err := json.Unmarshal(shippingRaw, &sa); err != nil {
		saPtr = nil
	}

	rows, err := w.Pool.Query(ctx, `
		SELECT oi.quantity, oi.unit_price::float8, p.name, p.image_url
		FROM order_items oi JOIN products p ON p.id = oi.product_id
		WHERE oi.order_id = $1
	`, job.Args.OrderID)
	if err != nil {
		return fmt.Errorf("load order items: %w", err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowToStructByName[orderLine])
	if err != nil {
		return fmt.Errorf("scan order items: %w", err)
	}

	recRows, err := w.Pool.Query(ctx, `SELECT name, image_url, price::float8 FROM products WHERE is_published = true LIMIT 3`)
	if err != nil {
		return fmt.Errorf("load recommendations: %w", err)
	}
	recs, err := pgx.CollectRows(recRows, pgx.RowToStructByName[recommendedProduct])
	if err != nil {
		return fmt.Errorf("scan recommendations: %w", err)
	}

	orderIDShort := strings.ToUpper(job.Args.OrderID[:8])
	isUnpaid := strings.ToLower(paymentStatus) == "unpaid" && paymentMethod != "cod"

	var buyerEmail string
	if saPtr != nil {
		buyerEmail = strings.TrimSpace(saPtr.Email)
	}

	if buyerEmail != "" {
		trackURL := fmt.Sprintf("%s/track-order?id=%s&email=%s", strings.TrimRight(w.SiteURL, "/"), job.Args.OrderID, buyerEmail)

		subject := fmt.Sprintf("Order confirmation #%s", orderIDShort)
		title := fmt.Sprintf("Thanks for your order, %s!", escapeHTML(saPtr.FirstName))
		banner := ""
		if isUnpaid {
			subject = fmt.Sprintf("Payment revision needed #%s", orderIDShort)
			title = "Payment revision needed"
			banner = `<div style="background:#fffcf5;border:1px solid #fbd200;padding:15px;margin-bottom:20px;border-radius:4px;">
				<p style="margin:0;color:#c45500;font-weight:700;font-size:15px;">Action Required: Update your payment method</p>
				<p style="margin:5px 0 0;font-size:13px;color:#111;">We're having trouble processing payment. Please update it to avoid cancellation.</p>
			</div>`
		}

		taxHTML := ""
		if kraPin != nil && *kraPin != "" {
			taxHTML = fmt.Sprintf(`<div style="background:#f8fafc;border:1px solid #e2e8f0;padding:10px;margin-bottom:20px;border-radius:4px;">
				<p style="margin:0;font-size:11px;font-weight:700;color:#555;text-transform:uppercase;">Tax Information</p>
				<p style="margin:5px 0 0;font-size:13px;color:#111;">KRA PIN: <strong>%s</strong></p>
			</div>`, escapeHTML(*kraPin))
		}

		ctaLabel := "Track Order"
		if isUnpaid {
			ctaLabel = "Update Payment"
		}

		html := fmt.Sprintf(`
			<div style="font-family:Arial,sans-serif;max-width:600px;margin:0 auto;background:#fff;">
				<div style="background:#232f3e;padding:15px 20px;"><img src="https://gizmojunction.com/logo-white.png" style="height:25px;" /></div>
				<div style="padding:20px;">
					<h1 style="font-size:20px;">%s</h1>
					%s
					<div style="display:flex;justify-content:space-between;border-bottom:1px solid #eee;padding-bottom:15px;margin-bottom:20px;">
						<div><p style="margin:0;font-size:12px;color:#555;font-weight:700;">DELIVER TO:</p>%s</div>
						<div style="text-align:right;"><p style="margin:0;font-size:12px;color:#555;font-weight:700;">ORDER #:</p><p style="margin:0;color:#007185;">%s</p></div>
					</div>
					%s
					%s
					<p style="text-align:right;font-size:16px;font-weight:700;">Grand Total: <span style="color:#b12704;">KSH %s</span></p>
					<div style="text-align:center;margin:30px 0;">
						<a href="%s" style="background:#ffd814;padding:12px 30px;color:#111;text-decoration:none;border-radius:8px;border:1px solid #fcd200;">%s</a>
					</div>
					<div style="background:#f9f9f9;padding:15px;border-radius:8px;">
						<p style="font-weight:700;margin-bottom:10px;">Top picks for you</p>
						%s
					</div>
				</div>
			</div>`,
			title, banner, formatShippingHTML(saPtr), orderIDShort, taxHTML, linesCardsHTML(lines),
			fmtMoney(totalAmount), trackURL, ctaLabel, recsHTML(recs))

		if err := w.Email.Send(ctx, EmailPayload{
			From:    "Gizmo Junction <noreply@notify.gizmojunction.com>",
			To:      []string{buyerEmail},
			Subject: subject,
			HTML:    html,
		}); err != nil {
			return fmt.Errorf("send buyer email: %w", err)
		}
	}

	status, statusLabel := "PAID", "(Paid)"
	if isUnpaid {
		status, statusLabel = "UNPAID", "(Unpaid)"
	}
	if err := w.Email.Send(ctx, EmailPayload{
		From:    "Gizmo Junction <noreply@notify.gizmojunction.com>",
		To:      []string{w.AdminEmail},
		Subject: fmt.Sprintf("New Order #%s %s", orderIDShort, statusLabel),
		HTML:    fmt.Sprintf("<h1>New Order</h1><p>ID: %s</p><p>Total: KES %s</p><p>Status: %s</p>", orderIDShort, fmtMoney(totalAmount), status),
	}); err != nil {
		return fmt.Errorf("send admin email: %w", err)
	}

	return nil
}
