package main

import "time"

const (
	ProfitBillingPosted  = "posted"
	ProfitBillingPending = "pending"

	ProfitReturnMerchantWarehouse   = "merchant_warehouse"
	ProfitReturnThirdPartyWarehouse = "third_party_warehouse"

	ProfitViolationFulfillmentDelay     = "fulfillment_delay"
	ProfitViolationFulfillmentFalseShip = "fulfillment_false_ship"

	ProfitFileShippingPosted   = "shipping_posted"
	ProfitFileShippingPending  = "shipping_pending"
	ProfitFileReturnMerchant   = "return_merchant"
	ProfitFileReturnThirdParty = "return_third_party"
	ProfitFileFundDetail       = "fund_detail"
	ProfitFileSettledFlow      = "settled_flow"
	ProfitFileSettledParent    = "settled_parent"
	ProfitFileUnsettle         = "unsettle"
)

// TemuProfitShippingLabelFee 映射发货面单费-已出账 / 发货面单费-待出账。
type TemuProfitShippingLabelFee struct {
	ShopKey         string
	BillingPhase    string
	PackageNo       string
	TrackingNo      string
	CarrierCode     string
	BillType        string
	Remark          string
	FreightAmount   float64
	Currency        string
	ReconcileStatus string
	OccurredAt      *time.Time
	ImportedAt      time.Time
}

// TemuProfitReturnLabelFee 映射退货面单费-退至商家仓 / 退货面单费-退至第三方仓。
type TemuProfitReturnLabelFee struct {
	ShopKey           string
	ReturnDestination string
	FundBillID        string
	TrackingNo        string
	PONo              string
	BillType          string
	Currency          string
	BillRemark        string
	Amount            float64
	OccurredAt        *time.Time
	ImportedAt        time.Time
}

// TemuProfitBuyerChargeback 映射对账中心-账务明细 / 支出-买家拒付。
type TemuProfitBuyerChargeback struct {
	ShopKey          string
	ViolationOrderNo string
	Amount           float64
	Currency         string
	AccountedAt      *time.Time
	ImportedAt       time.Time
}

// TemuProfitFulfillmentViolation 映射对账中心-账务明细 / 支出-履约违规-1、支出-履约违规-2。
type TemuProfitFulfillmentViolation struct {
	ShopKey       string
	SourceSheet   string
	ViolationNo   string
	OrderNo       string
	ViolationType string
	Amount        float64
	Currency      string
	AccountedAt   *time.Time
	ImportedAt    time.Time
}

// TemuProfitPlatformReturnLabelFee 映射对账中心-账务明细 / 其他-退货面单费平台承担。
type TemuProfitPlatformReturnLabelFee struct {
	ShopKey     string
	OrderNo     string
	Amount      float64
	Currency    string
	Remark      string
	AccountedAt *time.Time
	ImportedAt  time.Time
}

// TemuProfitDisposalFee 映射对账中心-账务明细 / 支出-处置费。
type TemuProfitDisposalFee struct {
	ShopKey      string
	SKUID        int64
	TrackingType string
	TrackingNo   string
	DestroyQty   int
	ReturnedAt   *time.Time
	DestroyFee   float64
	Currency     string
	AccountedAt  *time.Time
	ImportedAt   time.Time
}

// TemuProfitSettledFlow 映射对账中心-账务明细 / 结算，以及结算数据-已到账款项-PO明细账单。
type TemuProfitSettledFlow struct {
	ShopKey                       string
	FlowID                        string
	BatchNo                       string
	PONo                          string
	DocumentNo                    string
	TradeType                     string
	SettleAmount                  float64
	SKUID                         *int64
	SKUName                       string
	SKUExtCode                    string
	Quantity                      *float64
	DeclaredPrice                 *float64
	IsActivityPrice               *bool
	Currency                      string
	TotalDeclaredPrice            *float64
	SKUCouponAmount               float64
	ShopCouponAmount              float64
	DeclaredDiscountAmount        float64
	ActivityFreightDiscountAmount float64
	ReceivedAt                    *time.Time
	Remark                        string
	ImportedAt                    time.Time
}

// TemuProfitSettledParentOrder 映射结算数据-已到账款项-po聚合账单的 PO 金额行。
type TemuProfitSettledParentOrder struct {
	ShopKey                     string
	PONo                        string
	Currency                    string
	SalesReceipt                float64
	SalesReceiptAfterDiscount   float64
	SalesChargeback             float64
	FreightReceipt              float64
	FreightReceiptAfterDiscount float64
	FreightChargeback           float64
	SKUs                        []TemuProfitSettledParentSKU
	ImportedAt                  time.Time
}

// TemuProfitSettledParentSKU 映射 po 聚合账单「商品信息 * 销售件数」展开列，含合并单元格续行。
type TemuProfitSettledParentSKU struct {
	LineNo          int
	SKUID           int64
	SKUName         string
	SKUExtCode      string
	Quantity        float64
	DeclaredPrice   float64
	IsActivityPrice *bool
}

// TemuProfitUnsettleOrder 映射结算数据-待处理款项的 PO 金额行。
type TemuProfitUnsettleOrder struct {
	ShopKey                     string
	PONo                        string
	Currency                    string
	SalesReceipt                float64
	SalesReceiptAfterDiscount   float64
	SalesChargeback             float64
	FreightReceipt              float64
	FreightChargeback           float64
	FreightReceiptAfterDiscount float64
	SKUs                        []TemuProfitUnsettleSKU
	ImportedAt                  time.Time
}

// TemuProfitUnsettleSKU 映射待处理款项「商品信息 * 销售件数」展开列；该表没有 SKU货号、是否活动价。
type TemuProfitUnsettleSKU struct {
	LineNo        int
	SKUID         int64
	SKUName       string
	Quantity      float64
	DeclaredPrice float64
}

type ProfitImportBatch struct {
	File        string
	Kind        string
	Shipping    []TemuProfitShippingLabelFee
	Returns     []TemuProfitReturnLabelFee
	Chargebacks []TemuProfitBuyerChargeback
	Violations  []TemuProfitFulfillmentViolation
	Platforms   []TemuProfitPlatformReturnLabelFee
	Disposals   []TemuProfitDisposalFee
	Settled     []TemuProfitSettledFlow
	Parents     []TemuProfitSettledParentOrder
	Unsettles   []TemuProfitUnsettleOrder
	Warnings    []string
}

type ProfitTableStat struct {
	Table    string `json:"table"`
	Label    string `json:"label"`
	Rows     int    `json:"rows"`
	Inserted int    `json:"inserted"`
	Updated  int    `json:"updated"`
}

type ProfitFileImportResult struct {
	File     string            `json:"file"`
	Kind     string            `json:"kind"`
	Label    string            `json:"label"`
	Tables   []ProfitTableStat `json:"tables"`
	Warnings []string          `json:"warnings,omitempty"`
}

type ProfitImportResult struct {
	ShopKey       string                   `json:"shop_key"`
	SourceKind    string                   `json:"source_kind"`
	SourceName    string                   `json:"source_name"`
	Files         []ProfitFileImportResult `json:"files"`
	Tables        []ProfitTableStat        `json:"tables"`
	FilesImported int                      `json:"files_imported"`
	RowsUpserted  int                      `json:"rows_upserted"`
	Warnings      []string                 `json:"warnings,omitempty"`
}

type ProfitImportRun struct {
	ID            int64               `json:"id"`
	ShopKey       string              `json:"shop_key"`
	SourceKind    string              `json:"source_kind"`
	SourceName    string              `json:"source_name"`
	Status        string              `json:"status"`
	FilesImported int                 `json:"files_imported"`
	RowsUpserted  int                 `json:"rows_upserted"`
	Result        *ProfitImportResult `json:"result,omitempty"`
	ErrorMessage  string              `json:"error_message"`
	StartedAt     time.Time           `json:"started_at"`
	CompletedAt   *time.Time          `json:"completed_at,omitempty"`
}

type ProfitSummary struct {
	Tables       []ProfitTableCount `json:"tables"`
	LatestImport *ProfitImportRun   `json:"latest_import,omitempty"`
	Importing    bool               `json:"importing"`
}

type ProfitTableCount struct {
	Table string `json:"table"`
	Label string `json:"label"`
	Rows  int    `json:"rows"`
}
