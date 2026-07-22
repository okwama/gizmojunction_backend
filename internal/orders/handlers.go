package orders

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"gizmojunction/backend/internal/auth"
	"gizmojunction/backend/internal/jobs"
)

type Handlers struct {
	repo    *Repo
	authSvc *auth.Service
	river   *river.Client[pgx.Tx]
}

func Register(api huma.API, repo *Repo, authSvc *auth.Service, riverClient *river.Client[pgx.Tx]) {
	h := &Handlers{repo: repo, authSvc: authSvc, river: riverClient}

	huma.Register(api, huma.Operation{
		OperationID: "create-order",
		Method:      http.MethodPost,
		Path:        "/v1/orders",
		Summary:     "Create an order with its items (checkout)",
	}, h.CreateOrder)

	huma.Register(api, huma.Operation{
		OperationID: "checkout-order-summary",
		Method:      http.MethodGet,
		Path:        "/v1/orders/summary",
		Summary:     "Order summary for the checkout success page (order id + email)",
	}, h.CheckoutSummary)

	huma.Register(api, huma.Operation{
		OperationID: "track-order",
		Method:      http.MethodPost,
		Path:        "/v1/orders/track",
		Summary:     "Track an order by id prefix and checkout email",
	}, h.TrackOrder)

	huma.Register(api, huma.Operation{
		OperationID: "get-own-order",
		Method:      http.MethodGet,
		Path:        "/v1/me/orders/{id}",
		Summary:     "One of the signed-in user's orders with items",
	}, h.MyOrder)

	huma.Register(api, huma.Operation{
		OperationID: "admin-list-orders",
		Method:      http.MethodGet,
		Path:        "/v1/admin/orders",
		Summary:     "All orders with items and tax fields (admin only)",
	}, h.AdminList)

	huma.Register(api, huma.Operation{
		OperationID: "admin-get-order",
		Method:      http.MethodGet,
		Path:        "/v1/admin/orders/{id}",
		Summary:     "One order with items (admin only)",
	}, h.AdminGet)

	huma.Register(api, huma.Operation{
		OperationID: "admin-update-order",
		Method:      http.MethodPatch,
		Path:        "/v1/admin/orders/{id}",
		Summary:     "Update an order's status and/or KRA PIN (admin only)",
	}, h.AdminUpdate)

	huma.Register(api, huma.Operation{
		OperationID: "admin-sales-orders",
		Method:      http.MethodGet,
		Path:        "/v1/admin/sales/orders",
		Summary:     "Paid orders for the sales dashboard (admin only)",
	}, h.SalesOrders)

	huma.Register(api, huma.Operation{
		OperationID: "admin-reports",
		Method:      http.MethodGet,
		Path:        "/v1/admin/reports",
		Summary:     "Paid orders, order lines and inventory valuation for the reports page (admin only)",
	}, h.Reports)

	huma.Register(api, huma.Operation{
		OperationID: "admin-dashboard",
		Method:      http.MethodGet,
		Path:        "/v1/admin/dashboard",
		Summary:     "Dashboard stats: sales, orders, customers, low stock, best sellers, revenue history (admin only)",
	}, h.Dashboard)

	huma.Register(api, huma.Operation{
		OperationID: "admin-tax-orders",
		Method:      http.MethodGet,
		Path:        "/v1/admin/tax/orders",
		Summary:     "Orders joined with their tax invoice for the Tax page (admin only)",
	}, h.TaxOrders)
}

// --- Customer-facing ---

type CreateOrderInput struct {
	Authorization string `header:"Authorization,omitempty"`
	Body          NewOrder
}

type CreateOrderOutput struct {
	Body struct {
		ID string `json:"id"`
	}
}

func (h *Handlers) CreateOrder(ctx context.Context, input *CreateOrderInput) (*CreateOrderOutput, error) {
	if len(input.Body.Items) == 0 {
		return nil, huma.Error400BadRequest("order has no items")
	}
	if input.Body.PaymentMethod == "" {
		return nil, huma.Error400BadRequest("payment_method is required")
	}

	// Guest checkout stays allowed; a signed-in caller's identity comes
	// from the verified token, never the request body.
	var customerID *string
	if claims, err := h.authSvc.Authenticate(input.Authorization); err == nil {
		customerID = &claims.ProfileID
	}

	id, err := h.repo.CreateOrder(ctx, customerID, input.Body)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return nil, huma.Error400BadRequest(err.Error())
		}
		return nil, huma.Error500InternalServerError("failed to create order", err)
	}

	// COD/pay-on-collection orders have no payment webhook to trigger the
	// buyer/admin emails, so they fire on placement instead.
	if input.Body.PaymentMethod == "cod" && h.river != nil {
		if _, err := h.river.Insert(ctx, jobs.OrderNotificationArgs{OrderID: id}, nil); err != nil {
			log.Printf("orders: failed to enqueue COD notification for %s: %v", id, err)
		}
	}

	out := &CreateOrderOutput{}
	out.Body.ID = id
	return out, nil
}

type CheckoutSummaryInput struct {
	OrderID string `query:"order_id" required:"true"`
	Email   string `query:"email" required:"true"`
}

type CheckoutSummaryOutput struct {
	Body json.RawMessage
}

func (h *Handlers) CheckoutSummary(ctx context.Context, input *CheckoutSummaryInput) (*CheckoutSummaryOutput, error) {
	summary, err := h.repo.CheckoutSummary(ctx, input.OrderID, input.Email)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load order summary", err)
	}
	return &CheckoutSummaryOutput{Body: summary}, nil
}

type TrackOrderInput struct {
	Body struct {
		OrderID string `json:"order_id"`
		Email   string `json:"email"`
	}
}

type TrackOrderOutput struct {
	Body struct {
		Order *Order `json:"order,omitempty"`
	}
}

func (h *Handlers) TrackOrder(ctx context.Context, input *TrackOrderInput) (*TrackOrderOutput, error) {
	if input.Body.OrderID == "" || input.Body.Email == "" {
		return nil, huma.Error400BadRequest("order_id and email are required")
	}
	order, notFound, err := h.repo.TrackOrder(ctx, input.Body.Email, input.Body.OrderID)
	if err != nil {
		return nil, huma.Error500InternalServerError("tracking failed", err)
	}
	switch notFound {
	case "email":
		return nil, huma.Error404NotFound("No orders found for this email. Please ensure this is the email you used at checkout.")
	case "id":
		return nil, huma.Error404NotFound("Order ID not found for this email. Please check the ID in your email or the success page.")
	}
	out := &TrackOrderOutput{}
	out.Body.Order = order
	return out, nil
}

type MyOrderInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

type OrderOutput struct {
	Body Order
}

func (h *Handlers) MyOrder(ctx context.Context, input *MyOrderInput) (*OrderOutput, error) {
	claims, err := h.authSvc.Authenticate(input.Authorization)
	if err != nil {
		return nil, err
	}
	order, err := h.repo.OrderByID(ctx, input.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load order", err)
	}
	// Orders created before checkout attached customer ids have a NULL
	// customer_id — those stay reachable by id, matching prior behavior.
	if order == nil || (order.CustomerID != nil && *order.CustomerID != claims.ProfileID) {
		return nil, huma.Error404NotFound("Order not found.")
	}
	return &OrderOutput{Body: *order}, nil
}

// --- Admin ---

type adminAuthInput struct {
	Authorization string `header:"Authorization"`
}

func (h *Handlers) AdminList(ctx context.Context, input *adminAuthInput) (*struct{ Body []Order }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	orders, err := h.repo.AdminList(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load orders", err)
	}
	if orders == nil {
		orders = []Order{}
	}
	return &struct{ Body []Order }{Body: orders}, nil
}

type AdminGetInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

func (h *Handlers) AdminGet(ctx context.Context, input *AdminGetInput) (*OrderOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	order, err := h.repo.OrderByID(ctx, input.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load order", err)
	}
	if order == nil {
		return nil, huma.Error404NotFound("Order not found.")
	}
	return &OrderOutput{Body: *order}, nil
}

type AdminUpdateInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
	Body          struct {
		Status *string `json:"status,omitempty"`
		KraPin *string `json:"kra_pin,omitempty"`
	}
}

type successOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

func (h *Handlers) AdminUpdate(ctx context.Context, input *AdminUpdateInput) (*successOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.Status == nil && input.Body.KraPin == nil {
		return nil, huma.Error400BadRequest("nothing to update")
	}
	oldStatus, err := h.repo.UpdateOrder(ctx, input.ID, input.Body.Status, input.Body.KraPin)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("Order not found.")
		}
		return nil, huma.Error500InternalServerError("failed to update order", err)
	}

	// Side effects only on a genuine status transition, never on a repeat
	// save of the same status.
	if input.Body.Status != nil && *input.Body.Status != oldStatus {
		switch *input.Body.Status {
		case "SHIPPED":
			if h.river != nil {
				if _, err := h.river.Insert(ctx, jobs.OrderShippedNotificationArgs{OrderID: input.ID}, nil); err != nil {
					log.Printf("orders: failed to enqueue shipped notification for %s: %v", input.ID, err)
				}
			}
		case "READY_FOR_PICKUP":
			if h.river != nil {
				if _, err := h.river.Insert(ctx, jobs.OrderReadyForPickupArgs{OrderID: input.ID}, nil); err != nil {
					log.Printf("orders: failed to enqueue pickup notification for %s: %v", input.ID, err)
				}
			}
		case "CANCELLED":
			if err := h.repo.RestoreStock(ctx, input.ID); err != nil {
				log.Printf("orders: failed to restore stock for cancelled order %s: %v", input.ID, err)
			}
		}
	}

	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

func (h *Handlers) SalesOrders(ctx context.Context, input *adminAuthInput) (*struct{ Body []Order }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	orders, err := h.repo.PaidOrders(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load sales data", err)
	}
	if orders == nil {
		orders = []Order{}
	}
	return &struct{ Body []Order }{Body: orders}, nil
}

func (h *Handlers) Reports(ctx context.Context, input *adminAuthInput) (*struct{ Body ReportsData }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	data, err := h.repo.Reports(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load reports data", err)
	}
	return &struct{ Body ReportsData }{Body: data}, nil
}

func (h *Handlers) Dashboard(ctx context.Context, input *adminAuthInput) (*struct{ Body DashboardData }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	data, err := h.repo.Dashboard(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load dashboard data", err)
	}
	return &struct{ Body DashboardData }{Body: data}, nil
}

func (h *Handlers) TaxOrders(ctx context.Context, input *adminAuthInput) (*struct{ Body []Order }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	orders, err := h.repo.TaxOrders(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load tax orders", err)
	}
	if orders == nil {
		orders = []Order{}
	}
	return &struct{ Body []Order }{Body: orders}, nil
}
