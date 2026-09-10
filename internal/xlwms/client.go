// Package xlwms is a thin client for the xlwms-api-manager product-pairing
// API (https://pangutech.online/warehouse-console/api/product-pairings),
// which is the authoritative platform-SKU <-> warehouse-SKU mapping used by
// 领星 WMS for real fulfillment routing.
//
// The remote API is scoped by an "account" (an OpenAPI credential group) that
// has nothing to do with pangu-sales-manager's own domain model: a shop can
// be visible under more than one account, and different accounts return
// disjoint slices of the same catalog rather than conflicting data. Callers
// of this package never need to know about accounts — ListMappings and
// CreateMapping both iterate every known account internally and present a
// single merged/best-effort result.
package xlwms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultPageSize = 100

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

type Account struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
}

type PairingItem struct {
	SystemSKU string `json:"system_sku"`
	Quantity  int    `json:"quantity"`
}

type Pairing struct {
	PlatformSKU string        `json:"platform_sku"`
	StoreCode   string        `json:"store_code"`
	Items       []PairingItem `json:"items"`
}

// Mapping is a simplified single-item pairing: platform SKU maps to exactly
// one warehouse SKU with an integer quantity (conversion factor). Pairings
// with more than one item (bundle "recipes") are not representable here and
// are skipped by ListMappings.
type Mapping struct {
	PlatformSKU  string
	WarehouseSKU string
	Quantity     int
}

type apiEnvelope struct {
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

type pairingPageData struct {
	Records []Pairing `json:"records"`
	Pages   int       `json:"pages"`
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	fullURL := c.baseURL + path
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode xlwms request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, fullURL, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call xlwms %s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read xlwms response: %w", err)
	}
	var envelope apiEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode xlwms response (status %d): %w", response.StatusCode, err)
	}
	if !envelope.Success {
		message := envelope.Error
		if message == "" {
			message = fmt.Sprintf("xlwms request failed with status %d", response.StatusCode)
		}
		return fmt.Errorf("xlwms %s %s: %s", method, path, message)
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode xlwms data: %w", err)
		}
	}
	return nil
}

// ListAccounts returns every configured OMS account, regardless of which
// stores it can see.
func (c *Client) ListAccounts(ctx context.Context) ([]Account, error) {
	var accounts []Account
	if err := c.doJSON(ctx, http.MethodGet, "/platform-orders/accounts", nil, nil, &accounts); err != nil {
		return nil, err
	}
	return accounts, nil
}

func (c *Client) listProductPairingsPage(ctx context.Context, account, storeCode string, page int) (pairingPageData, error) {
	query := url.Values{
		"account":    {account},
		"store_code": {storeCode},
		"page":       {strconv.Itoa(page)},
		"page_size":  {strconv.Itoa(defaultPageSize)},
	}
	var result pairingPageData
	err := c.doJSON(ctx, http.MethodGet, "/product-pairings", query, nil, &result)
	return result, err
}

// ListMappings returns the union of single-item platform-SKU mappings for
// storeCode across every known account, keyed by platform SKU. Bundle
// pairings (more than one item) are counted in skippedBundles and otherwise
// ignored. Callers do not need to pick an account: this walks all of them.
func (c *Client) ListMappings(ctx context.Context, storeCode string) (mappings map[string]Mapping, skippedBundles int, err error) {
	accounts, err := c.ListAccounts(ctx)
	if err != nil {
		return nil, 0, err
	}
	mappings = make(map[string]Mapping)
	for _, account := range accounts {
		for page := 1; ; page++ {
			result, pageErr := c.listProductPairingsPage(ctx, account.Key, storeCode, page)
			if pageErr != nil {
				return nil, 0, fmt.Errorf("list product pairings for account %s: %w", account.Key, pageErr)
			}
			for _, record := range result.Records {
				if len(record.Items) != 1 {
					skippedBundles++
					continue
				}
				item := record.Items[0]
				mappings[record.PlatformSKU] = Mapping{
					PlatformSKU:  record.PlatformSKU,
					WarehouseSKU: item.SystemSKU,
					Quantity:     item.Quantity,
				}
			}
			if page >= result.Pages || len(result.Records) == 0 {
				break
			}
		}
	}
	return mappings, skippedBundles, nil
}

// CreateMapping registers a single-item platform-SKU -> warehouse-SKU
// pairing. It tries every known account and succeeds as soon as one accepts
// the write; the caller never needs to know which account that was. It
// returns an error only if every account rejected the write.
func (c *Client) CreateMapping(ctx context.Context, storeCode, platformSKU, warehouseSKU string, quantity int) error {
	accounts, err := c.ListAccounts(ctx)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return fmt.Errorf("xlwms has no configured OMS accounts")
	}
	body := map[string]any{
		"store_code":   storeCode,
		"platform_sku": platformSKU,
		"items":        []PairingItem{{SystemSKU: warehouseSKU, Quantity: quantity}},
	}
	var errs []string
	for _, account := range accounts {
		requestBody := map[string]any{"account": account.Key}
		for key, value := range body {
			requestBody[key] = value
		}
		if err := c.doJSON(ctx, http.MethodPost, "/product-pairings", nil, requestBody, nil); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		return nil
	}
	return fmt.Errorf("xlwms rejected mapping write on all %d accounts: %s", len(accounts), strings.Join(errs, "; "))
}
