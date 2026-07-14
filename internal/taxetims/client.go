package taxetims

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client wraps KRA's OSCU API. Base URL and field names come from KRA's
// public OSCU Specification Document v2.0
// (https://www.kra.go.ke/images/publications/OSCU_Specification_Document_v2.0.pdf)
// and community reference implementations, not a first-party Swagger file —
// verify these against the real sandbox Postman collection once the owner
// completes KRA device registration (BRD certification roadmap step 1-2)
// before relying on this for a real certification submission.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

const (
	sandboxBaseURL    = "https://etims-api-sbx.kra.go.ke/etims-api"
	productionBaseURL = "https://etims-api.kra.go.ke/etims-api"
)

// NewClient builds an OSCU client. env is "sandbox" or "production"; any
// other value (including empty) defaults to sandbox as the safer choice.
func NewClient(env string) *Client {
	base := sandboxBaseURL
	if env == "production" {
		base = productionBaseURL
	}
	return &Client{
		baseURL:    base,
		httpClient: &http.Client{Timeout: 8 * time.Second}, // BRD NFR: "OSCU API call must complete within 5 seconds; timeout after 8 seconds"
	}
}

type InitializeRequest struct {
	TIN      string `json:"tin"`
	BhfID    string `json:"bhfId"`
	DvcSrlNo string `json:"dvcSrlNo"`
}

type InitializeResponse struct {
	ResultCd  string `json:"resultCd"`
	ResultMsg string `json:"resultMsg"`
	Data      struct {
		CmcKey string `json:"cmcKey"`
		SdcID  string `json:"sdcId"`
		MrcNo  string `json:"mrcNo"`
	} `json:"data"`
}

// Initialize performs the one-time OSCU device registration call. Run once
// by an admin, from the admin Tax page, after the owner has real KRA
// sandbox/production credentials — there is nothing to configure until then.
func (c *Client) Initialize(ctx context.Context, req InitializeRequest) (InitializeResponse, error) {
	var out InitializeResponse
	if err := c.post(ctx, "/initializer/selectInitInfo", nil, req, &out); err != nil {
		return InitializeResponse{}, err
	}
	if out.ResultCd != "" && out.ResultCd != "000" {
		return InitializeResponse{}, fmt.Errorf("oscu initialize failed: %s (%s)", out.ResultMsg, out.ResultCd)
	}
	return out, nil
}

type SalesLineItem struct {
	ItemSeq int32   `json:"itemSeq"`
	ItemCd  string  `json:"itemCd"`
	ItemNm  string  `json:"itemNm"`
	Qty     float64 `json:"qty"`
	Prc     float64 `json:"prc"`
	SplyAmt float64 `json:"splyAmt"`
	TaxTyCd string  `json:"taxTyCd"` // "A" exempt, "B" 16% standard, "C" zero-rated — per OSCU tax type codes
	TaxAmt  float64 `json:"taxAmt"`
	TotAmt  float64 `json:"totAmt"`
}

type SalesRequest struct {
	TIN         string          `json:"tin"`
	BhfID       string          `json:"bhfId"`
	InvcNo      string          `json:"invcNo"`
	CustTin     string          `json:"custTin,omitempty"`
	SalesDt     string          `json:"salesDt"` // YYYYMMDD
	TotItemCnt  int             `json:"totItemCnt"`
	TaxblAmtB   float64         `json:"taxblAmtB"`
	TaxAmtB     float64         `json:"taxAmtB"`
	TotTaxblAmt float64         `json:"totTaxblAmt"`
	TotTaxAmt   float64         `json:"totTaxAmt"`
	TotAmt      float64         `json:"totAmt"`
	PmtTyCd     string          `json:"pmtTyCd"` // payment method code
	ItemList    []SalesLineItem `json:"itemList"`
}

type SalesResponse struct {
	ResultCd  string `json:"resultCd"`
	ResultMsg string `json:"resultMsg"`
	Data      struct {
		CuInvcNo  string `json:"curRcptNo"` // KRA's CU Invoice Number (CUIN)
		RcptSign  string `json:"rcptSign"`  // receipt signature
		IntrlData string `json:"intrlData"` // internal data field
		SdcID     string `json:"sdcId"`
		MrcNo     string `json:"mrcNo"`
	} `json:"data"`
}

// SubmitSale submits a sales transaction (TrnsSalesSaveWrReq equivalent).
// cmcKey authenticates the request per KRA's device-bound signing scheme.
func (c *Client) SubmitSale(ctx context.Context, cmcKey string, req SalesRequest) (SalesResponse, error) {
	var out SalesResponse
	headers := map[string]string{"cmcKey": cmcKey}
	if err := c.post(ctx, "/trnsSales/saveSales", headers, req, &out); err != nil {
		return SalesResponse{}, err
	}
	if out.ResultCd != "" && out.ResultCd != "000" {
		return SalesResponse{}, fmt.Errorf("oscu sale submission failed: %s (%s)", out.ResultMsg, out.ResultCd)
	}
	return out, nil
}

func (c *Client) post(ctx context.Context, path string, headers map[string]string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("oscu request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read oscu response: %w", err)
	}

	if resp.StatusCode >= 300 {
		return fmt.Errorf("oscu returned status %d: %s", resp.StatusCode, string(respBody))
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode oscu response: %w", err)
	}
	return nil
}
