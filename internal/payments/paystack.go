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
		Amount   float64         `json:"amount"`
		Email    string          `json:"email"`
		OrderID  string          `json:"orderId"`
		Metadata json.RawMessage `json:"metadata,omitempty"`
	}
}

type PaystackInitOutput struct {
	Body json.RawMessage
}

func (d *Deps) PaystackInit(ctx context.Context, input *PaystackInitInput) (*PaystackInitOutput, error) {
	if input.Body.Amount <= 0 || input.Body.Email == "" || input.Body.OrderID == "" {
		return nil, huma.Error400BadRequest("Missing required fields: amount, email, or orderId")
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
		"amount":    int(input.Body.Amount*100 + 0.5), // Paystack expects sub-units
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
			Reference string `json:"reference"`
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

	if event.Event == "charge.success" && event.Data.Metadata.OrderID != "" {
		ctx := r.Context()
		meta, _ := json.Marshal(map[string]string{
			"paystack_event": event.Event,
			"paystack_ref":   event.Data.Reference,
			"completed_at":   time.Now().UTC().Format(time.RFC3339),
		})

		var orderID string
		err := d.Orders.QueryRow(ctx, `
			UPDATE orders SET payment_status = 'paid', status = 'PROCESSING', payment_metadata = $2
			WHERE id = $1 AND payment_status IS DISTINCT FROM 'paid'
			RETURNING id::text`, event.Data.Metadata.OrderID, meta).Scan(&orderID)
		if err != nil {
			log.Printf("payments: paystack charge.success for %s matched no unpaid order (duplicate or unknown): %v", event.Data.Metadata.OrderID, err)
		} else {
			log.Printf("payments: order %s marked as paid via Paystack", orderID)
			d.firePaidSideEffects(ctx, orderID)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
}
