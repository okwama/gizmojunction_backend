// Package payments is the Phase 6 port of the four payment Edge Functions
// (mpesa-stk-push, mpesa-callback, paystack-init, paystack-webhook).
// Webhook processing gains what the Deno versions never had: idempotent
// paid-state transitions (a duplicate callback can't double-fire emails or
// tax submissions) and in-process river enqueues instead of HTTP hops.
//
// Cutover model: M-Pesa's callback URL travels inside each STK push
// request, so M-Pesa flips the moment the frontend initiates via this
// package. Paystack's webhook URL is a dashboard setting — flip it to
// {BACKEND_PUBLIC_URL}/v1/payments/paystack/webhook when ready; the Deno
// function stays deployed as rollback.
package payments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

func (d *Deps) mpesaBaseURL() string {
	if d.Cfg.MpesaEnvironment == "production" {
		return "https://api.safaricom.co.ke"
	}
	return "https://sandbox.safaricom.co.ke"
}

func (d *Deps) mpesaAccessToken(ctx context.Context) (string, error) {
	auth := base64.StdEncoding.EncodeToString([]byte(d.Cfg.MpesaConsumerKey + ":" + d.Cfg.MpesaConsumerSecret))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		d.mpesaBaseURL()+"/oauth/v1/generate?grant_type=client_credentials", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Basic "+auth)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("payments: safaricom token error: %s", string(body))
		return "", fmt.Errorf("failed to get M-Pesa access token: %s", resp.Status)
	}

	var parsed struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	return parsed.AccessToken, nil
}

type StkPushInput struct {
	Body struct {
		Amount  float64 `json:"amount"`
		Phone   string  `json:"phone"`
		OrderID string  `json:"orderId"`
	}
}

type StkPushOutput struct {
	Body json.RawMessage
}

func (d *Deps) StkPush(ctx context.Context, input *StkPushInput) (*StkPushOutput, error) {
	if input.Body.Amount <= 0 || input.Body.Phone == "" || input.Body.OrderID == "" {
		return nil, huma.Error400BadRequest("Missing required fields: amount, phone, or orderId")
	}

	token, err := d.mpesaAccessToken(ctx)
	if err != nil {
		return nil, huma.Error502BadGateway(err.Error())
	}

	timestamp := time.Now().UTC().Format("20060102150405")
	password := base64.StdEncoding.EncodeToString([]byte(d.Cfg.MpesaShortcode + d.Cfg.MpesaPasskey + timestamp))

	// Buy Goods Till mode, matching the Deno function's enforced setting.
	payload, _ := json.Marshal(map[string]any{
		"BusinessShortCode": d.Cfg.MpesaShortcode,
		"Password":          password,
		"Timestamp":         timestamp,
		"TransactionType":   "CustomerBuyGoodsOnline",
		"Amount":            int(input.Body.Amount + 0.5),
		"PartyA":            input.Body.Phone,
		"PartyB":            d.Cfg.MpesaTillNumber,
		"PhoneNumber":       input.Body.Phone,
		"CallBackURL":       strings.TrimRight(d.Cfg.BackendPublicURL, "/") + "/v1/payments/mpesa/callback",
		"AccountReference":  "ORD-" + input.Body.OrderID,
		"TransactionDesc":   "Payment for Order " + input.Body.OrderID,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.mpesaBaseURL()+"/mpesa/stkpush/v1/processrequest", strings.NewReader(string(payload)))
	if err != nil {
		return nil, huma.Error500InternalServerError("stk request build failed", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, huma.Error502BadGateway("M-Pesa request failed: " + err.Error())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, huma.Error502BadGateway("M-Pesa response unreadable")
	}

	var stk struct {
		ResponseCode        string `json:"ResponseCode"`
		ResponseDescription string `json:"ResponseDescription"`
		CheckoutRequestID   string `json:"CheckoutRequestID"`
	}
	if err := json.Unmarshal(raw, &stk); err != nil || stk.ResponseCode != "0" {
		desc := stk.ResponseDescription
		if desc == "" {
			desc = "M-Pesa STK Push failed"
		}
		log.Printf("payments: STK push rejected: %s", string(raw))
		return nil, huma.Error400BadRequest(desc)
	}

	meta, _ := json.Marshal(map[string]string{"mpesa_checkout_id": stk.CheckoutRequestID})
	if _, err := d.Orders.Exec(ctx, `
		UPDATE orders SET payment_intent_id = $2, payment_method = 'mpesa', payment_status = 'pending', payment_metadata = $3
		WHERE id = $1`, input.Body.OrderID, stk.CheckoutRequestID, meta); err != nil {
		return nil, huma.Error500InternalServerError("Order update failed", err)
	}

	return &StkPushOutput{Body: raw}, nil
}

// handleMpesaCallback receives Daraja's async result. Registered on the raw
// mux — Daraja is the caller, not a browser.
func (d *Deps) handleMpesaCallback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Body struct {
			StkCallback struct {
				ResultCode        int    `json:"ResultCode"`
				CheckoutRequestID string `json:"CheckoutRequestID"`
				CallbackMetadata  struct {
					Item []struct {
						Name  string          `json:"Name"`
						Value json.RawMessage `json:"Value"`
					} `json:"Item"`
				} `json:"CallbackMetadata"`
			} `json:"stkCallback"`
		} `json:"Body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Body.StkCallback.CheckoutRequestID == "" {
		log.Printf("payments: invalid M-Pesa callback payload")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid payload"})
		return
	}

	cb := body.Body.StkCallback
	ctx := r.Context()

	if cb.ResultCode == 0 {
		items := map[string]json.RawMessage{}
		for _, item := range cb.CallbackMetadata.Item {
			items[item.Name] = item.Value
		}
		meta, _ := json.Marshal(map[string]any{
			"mpesa_receipt": jsonString(items["MpesaReceiptNumber"]),
			"mpesa_amount":  json.RawMessage(orNull(items["Amount"])),
			"mpesa_phone":   json.RawMessage(orNull(items["PhoneNumber"])),
			"completed_at":  time.Now().UTC().Format(time.RFC3339),
		})

		// Idempotency the Deno version lacked: only the row that actually
		// transitions to paid fires the side effects below. A duplicate
		// callback matches zero rows and exits quietly.
		var orderID string
		err := d.Orders.QueryRow(ctx, `
			UPDATE orders SET status = 'PROCESSING', payment_status = 'paid', payment_metadata = $2
			WHERE payment_intent_id = $1 AND payment_status IS DISTINCT FROM 'paid'
			RETURNING id::text`, cb.CheckoutRequestID, meta).Scan(&orderID)
		if err != nil {
			log.Printf("payments: mpesa callback for %s matched no unpaid order (duplicate or unknown): %v", cb.CheckoutRequestID, err)
			writeJSON(w, http.StatusOK, map[string]any{"ResultCode": 0, "ResultDesc": "Success"})
			return
		}
		log.Printf("payments: order %s marked as paid via M-Pesa", orderID)
		d.firePaidSideEffects(ctx, orderID)
	} else {
		if _, err := d.Orders.Exec(ctx, `
			UPDATE orders SET payment_status = 'failed'
			WHERE payment_intent_id = $1 AND payment_status IS DISTINCT FROM 'paid'`, cb.CheckoutRequestID); err != nil {
			log.Printf("payments: failed to mark mpesa failure: %v", err)
		}
	}

	// Daraja expects this exact acknowledgment shape.
	writeJSON(w, http.StatusOK, map[string]any{"ResultCode": 0, "ResultDesc": "Success"})
}

func jsonString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func orNull(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("null")
	}
	return raw
}
