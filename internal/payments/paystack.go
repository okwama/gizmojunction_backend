package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

type PaystackInitInput struct {
	Origin string `header:"Origin"`
	Body   struct {
		// Amount is accepted for API compatibility but ignored — the
		// charge always uses the order row's total (which includes the
		// shipping fee the old client-supplied subtotal silently
		// dropped).
		Amount   float64         `json:"amount,omitempty" required:"false"`
		Email    string          `json:"email"`
		OrderID  string          `json:"orderId"`
		Metadata json.RawMessage `json:"metadata,omitempty"`
	}
}

type PaystackInitOutput struct {
	Body json.RawMessage
}

func (d *Deps) PaystackInit(ctx context.Context, input *PaystackInitInput) (*PaystackInitOutput, error) {
	if input.Body.Email == "" || input.Body.OrderID == "" {
		return nil, huma.Error400BadRequest("Missing required fields: email or orderId")
	}

	total, err := d.orderChargeAmount(ctx, input.Body.OrderID)
	if err != nil {
		return nil, err
	}

	origin := input.Origin
	if origin == "" {
		origin = d.Cfg.SiteURL
	}

	metadata := map[string]any{"order_id": input.Body.OrderID}
	if len(input.Body.Metadata) > 0 {
		var extra map[string]any
		if json.Unmarshal(input.Body.Metadata, &extra) == nil {
			for k, v := range extra {
				metadata[k] = v
			}
			metadata["order_id"] = input.Body.OrderID
		}
	}

	payload, _ := json.Marshal(map[string]any{
		"amount":    int(total*100 + 0.5), // Paystack expects sub-units
		"email":     input.Body.Email,
		"reference": fmt.Sprintf("ORD-%s-%d", input.Body.OrderID, time.Now().UnixMilli()),
		"metadata":  metadata,
		"callback_url": fmt.Sprintf("%s/checkout/success?id=%s&email=%s&method=card",
			strings.TrimRight(origin, "/"), input.Body.OrderID, strings.ReplaceAll(input.Body.Email, "+", "%2B")),
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.paystack.co/transaction/initialize", strings.NewReader(string(payload)))
	if err != nil {
		return nil, huma.Error500InternalServerError("paystack request build failed", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.Cfg.PaystackSecretKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, huma.Error502BadGateway("Paystack request failed: " + err.Error())
	}
	defer resp.Body.Close()

	var parsed struct {
		Status  bool            `json:"status"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, huma.Error502BadGateway("Paystack response unreadable")
	}
	if resp.StatusCode >= 400 || !parsed.Status {
		msg := parsed.Message
		if msg == "" {
			msg = "Paystack initialization failed"
		}
		return nil, huma.Error400BadRequest(msg)
	}

	var data struct {
		Reference string `json:"reference"`
	}
	_ = json.Unmarshal(parsed.Data, &data)

	if _, err := d.Orders.Exec(ctx, `
		UPDATE orders SET payment_intent_id = $2, payment_method = 'card', payment_status = 'pending'
		WHERE id = $1`, input.Body.OrderID, data.Reference); err != nil {
		return nil, huma.Error500InternalServerError("Failed to update order with payment reference", err)
	}

	return &PaystackInitOutput{Body: parsed.Data}, nil
}

// handlePaystackWebhook verifies Paystack's HMAC-SHA512 signature over the
// raw body, then applies the same idempotent paid transition as M-Pesa.
func (d *Deps) handlePaystackWebhook(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unreadable body"})
		return
	}

	mac := hmac.New(sha512.New, []byte(d.Cfg.PaystackSecretKey))
	mac.Write(rawBody)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(r.Header.Get("x-paystack-signature"))) {
		log.Printf("payments: invalid Paystack signature received")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid signature"})
		return
	}

	var event struct {
		Event string `json:"event"`
		Data  struct {
			Reference string  `json:"reference"`
			Amount    float64 `json:"amount"` // sub-units (cents)
			Metadata  struct {
				OrderID string `json:"order_id"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &event); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}
	log.Printf("payments: Paystack webhook event: %s", event.Event)

	ctx := r.Context()
	switch {
	case event.Event == "charge.success" && event.Data.Metadata.OrderID != "":
		meta, _ := json.Marshal(map[string]string{
			"paystack_event": event.Event,
			"paystack_ref":   event.Data.Reference,
			"completed_at":   time.Now().UTC().Format(time.RFC3339),
		})

		// Same amount guard as M-Pesa: the paid amount (in sub-units)
		// must match the order total or the order stays unpaid for
		// manual review. Amount 0 (absent from payload) skips the check.
		var orderID string
		err := d.Orders.QueryRow(ctx, `
			UPDATE orders SET payment_status = 'paid', status = 'PROCESSING', payment_metadata = $2
			WHERE id = $1 AND payment_status IS DISTINCT FROM 'paid'
			  AND ($3 <= 0 OR abs(total_amount * 100 - $3) <= 100)
			RETURNING id::text`, event.Data.Metadata.OrderID, meta, event.Data.Amount).Scan(&orderID)
		if err != nil {
			log.Printf("payments: paystack charge.success for %s (amount %.0f) matched no unpaid order with that total (duplicate, unknown, or amount mismatch): %v",
				event.Data.Metadata.OrderID, event.Data.Amount, err)
		} else {
			log.Printf("payments: order %s marked as paid via Paystack", orderID)
			d.firePaidSideEffects(ctx, orderID)
		}

	case event.Event == "charge.failed" && event.Data.Metadata.OrderID != "":
		// Surface failed card charges so the success page can leave its
		// "awaiting payment" state instead of polling into the void.
		if _, err := d.Orders.Exec(ctx, `
			UPDATE orders SET payment_status = 'failed'
			WHERE id = $1 AND payment_status IS DISTINCT FROM 'paid'`, event.Data.Metadata.OrderID); err != nil {
			log.Printf("payments: failed to mark paystack failure for %s: %v", event.Data.Metadata.OrderID, err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
}
