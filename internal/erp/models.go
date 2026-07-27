package erp

import "time"

type Supplier struct {
	ID        string    `db:"id" json:"id"`
	Name      string    `db:"name" json:"name"`
	Contact   *string   `db:"contact" json:"contact,omitempty"`
	Email     *string   `db:"email" json:"email,omitempty"`
	Phone     *string   `db:"phone" json:"phone,omitempty"`
	Address   *string   `db:"address" json:"address,omitempty"`
	Terms     *string   `db:"terms" json:"terms,omitempty"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type PurchaseOrder struct {
	ID           string    `db:"id" json:"id"`
	SupplierID   *string   `db:"supplier_id" json:"supplier_id,omitempty"`
	SupplierName *string   `db:"supplier_name" json:"supplier_name,omitempty"`
	Status       string    `db:"status" json:"status"`
	TotalAmount  float64   `db:"total_amount" json:"total_amount"`
	Notes        *string   `db:"notes" json:"notes,omitempty"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
}

// RecentProduct feeds the ERP page's "Recent Activity Log", which has always
// been the 10 most recently updated products dressed up as stock movements —
// a faithful port of the page's existing client-side mapping, not a real
// inventory ledger.
type RecentProduct struct {
	ID        string    `db:"id" json:"id"`
	SKU       string    `db:"sku" json:"sku"`
	Name      string    `db:"name" json:"name"`
	StockQty  int32     `db:"stock_quantity" json:"stock_quantity"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

type Stats struct {
	TotalSKUs  int     `json:"total_skus"`
	TotalValue float64 `json:"total_value"`
}

type Overview struct {
	Stats          Stats           `json:"stats"`
	RecentProducts []RecentProduct `json:"recent_products"`
	Suppliers      []Supplier      `json:"suppliers"`
	PurchaseOrders []PurchaseOrder `json:"purchase_orders"`
}

type lpoItem struct {
	Name     string  `db:"name"`
	SKU      string  `db:"sku"`
	Quantity int32   `db:"quantity"`
	UnitCost float64 `db:"unit_cost"`
}

type lpoData struct {
	ID            string
	CreatedAt     time.Time
	TotalAmount   float64
	SupplierName  string
	SupplierEmail string
	SupplierPhone string
	Items         []lpoItem
}

// POItemInput is a line item supplied when creating a purchase order.
type POItemInput struct {
	ProductID string
	Quantity  int32
	UnitCost  float64
}

// ReceivedItemInput is one line of a goods-received submission.
type ReceivedItemInput struct {
	PurchaseOrderItemID string
	QuantityReceived    int32
}

// PurchaseOrderItemDetail is one line item on the receive page: what was
// ordered, what's already been received, and what's left.
type PurchaseOrderItemDetail struct {
	ID               string  `db:"id" json:"id"`
	ProductID        *string `db:"product_id" json:"product_id,omitempty"`
	Name             string  `db:"name" json:"name"`
	SKU              string  `db:"sku" json:"sku"`
	Quantity         int32   `db:"quantity" json:"quantity"`
	UnitCost         float64 `db:"unit_cost" json:"unit_cost"`
	ReceivedQuantity int32   `db:"received_quantity" json:"received_quantity"`
}

type PurchaseOrderDetail struct {
	ID           string                    `json:"id"`
	SupplierID   *string                   `json:"supplier_id,omitempty"`
	SupplierName *string                   `json:"supplier_name,omitempty"`
	Status       string                    `json:"status"`
	TotalAmount  float64                   `json:"total_amount"`
	Notes        *string                   `json:"notes,omitempty"`
	CreatedAt    time.Time                 `json:"created_at"`
	Items        []PurchaseOrderItemDetail `json:"items"`
}
