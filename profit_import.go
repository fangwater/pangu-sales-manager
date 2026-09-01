package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

const profitUploadMaxBytes = 32 << 20

var profitFileLabels = map[string]string{
	ProfitFileShippingPosted:   "发货面单费-已出账",
	ProfitFileShippingPending:  "发货面单费-待出账",
	ProfitFileReturnMerchant:   "退货面单费-退至商家仓",
	ProfitFileReturnThirdParty: "退货面单费-退至第三方仓",
	ProfitFileFundDetail:       "对账中心-账务明细",
	ProfitFileSettledFlow:      "结算数据-已到账款项-PO明细账单",
	ProfitFileSettledParent:    "结算数据-已到账款项-po聚合账单",
	ProfitFileUnsettle:         "结算数据-待处理款项",
}

var profitTimeLayouts = []string{
	"2006-01-02 15:04:05.000",
	"2006-01-02 15:04:05.00",
	"2006-01-02 15:04:05.0",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	time.RFC3339,
}

type profitParseOptions struct {
	ShopKey  string
	Hint     string
	Location *time.Location
}

func parseProfitUpload(filename string, data []byte, opts profitParseOptions) (string, []ProfitImportBatch, error) {
	if opts.Location == nil {
		opts.Location = time.FixedZone("CST", 8*60*60)
	}
	kind := detectUploadSourceKind(filename, data)
	switch kind {
	case "zip":
		batches, err := parseProfitZip(data, opts)
		return kind, batches, err
	case "xlsx":
		batch, err := parseProfitWorkbook(filename, data, opts)
		if err != nil {
			return kind, nil, err
		}
		return kind, []ProfitImportBatch{batch}, nil
	default:
		return "", nil, fmt.Errorf("仅支持 .xlsx 或 .zip")
	}
}

func detectUploadSourceKind(filename string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".xlsx" {
		return "xlsx"
	}
	if ext == ".zip" {
		return "zip"
	}
	if len(data) >= 4 && data[0] == 'P' && data[1] == 'K' {
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return ""
		}
		for _, file := range reader.File {
			if file.Name == "[Content_Types].xml" || strings.HasPrefix(file.Name, "xl/") {
				return "xlsx"
			}
		}
		return "zip"
	}
	return ""
}

func parseProfitZip(data []byte, opts profitParseOptions) ([]ProfitImportBatch, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("无法读取 zip: %w", err)
	}
	var batches []ProfitImportBatch
	for _, file := range reader.File {
		if skipProfitZipEntry(file.Name) {
			continue
		}
		entry, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("打开 %s: %w", file.Name, err)
		}
		payload, err := io.ReadAll(entry)
		entry.Close()
		if err != nil {
			return nil, fmt.Errorf("读取 %s: %w", file.Name, err)
		}
		batch, err := parseProfitWorkbook(filepath.Base(file.Name), payload, opts)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(file.Name), err)
		}
		batches = append(batches, batch)
	}
	if len(batches) == 0 {
		return nil, fmt.Errorf("zip 中没有可导入的 xlsx")
	}
	return batches, nil
}

func skipProfitZipEntry(name string) bool {
	clean := filepath.ToSlash(name)
	if strings.Contains(clean, "..") {
		return true
	}
	base := filepath.Base(clean)
	if strings.HasPrefix(base, "._") || base == ".DS_Store" {
		return true
	}
	if strings.HasPrefix(clean, "__MACOSX/") || strings.Contains(clean, "/__MACOSX/") {
		return true
	}
	return !strings.EqualFold(filepath.Ext(base), ".xlsx")
}

func parseProfitWorkbook(filename string, data []byte, opts profitParseOptions) (ProfitImportBatch, error) {
	book, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return ProfitImportBatch{}, fmt.Errorf("无法读取 Excel: %w", err)
	}
	defer book.Close()

	sheets := book.GetSheetList()
	if len(sheets) == 0 {
		return ProfitImportBatch{}, fmt.Errorf("工作簿没有工作表")
	}
	firstRows, err := book.GetRows(sheets[0])
	if err != nil {
		return ProfitImportBatch{}, fmt.Errorf("读取工作表 %s: %w", sheets[0], err)
	}
	kind, err := detectProfitFileKind(filename, opts.Hint, sheets, firstRows)
	if err != nil {
		return ProfitImportBatch{}, err
	}
	batch := ProfitImportBatch{File: filename, Kind: kind}
	switch kind {
	case ProfitFileShippingPosted:
		batch.Shipping, err = parseShippingLabelFees(firstRows, opts.ShopKey, ProfitBillingPosted, opts.Location)
	case ProfitFileShippingPending:
		batch.Shipping, err = parseShippingLabelFees(firstRows, opts.ShopKey, ProfitBillingPending, opts.Location)
	case ProfitFileReturnMerchant:
		batch.Returns, err = parseReturnLabelFees(firstRows, opts.ShopKey, ProfitReturnMerchantWarehouse, opts.Location)
	case ProfitFileReturnThirdParty:
		batch.Returns, err = parseReturnLabelFees(firstRows, opts.ShopKey, ProfitReturnThirdPartyWarehouse, opts.Location)
	case ProfitFileSettledFlow:
		batch.Settled, err = parseSettledFlows(firstRows, opts.ShopKey, opts.Location)
	case ProfitFileSettledParent:
		batch.Parents, err = parseSettledParentOrders(firstRows, opts.ShopKey)
	case ProfitFileUnsettle:
		batch.Unsettles, err = parseUnsettleOrders(firstRows, opts.ShopKey)
	case ProfitFileFundDetail:
		err = parseFundDetailWorkbook(book, sheets, opts, &batch)
	default:
		err = fmt.Errorf("无法识别表格类型")
	}
	if err != nil {
		return ProfitImportBatch{}, err
	}
	return batch, nil
}

func detectProfitFileKind(filename, hint string, sheets []string, firstRows [][]string) (string, error) {
	if hint != "" {
		if _, ok := profitFileLabels[hint]; !ok {
			return "", fmt.Errorf("未知表格类型 %s", hint)
		}
		return hint, nil
	}
	if kind := detectProfitKindFromName(filename); kind != "" {
		return kind, nil
	}
	if kind := detectProfitKindFromSheets(sheets); kind != "" {
		return kind, nil
	}
	if kind := detectProfitKindFromHeaders(firstRows); kind != "" {
		return kind, nil
	}
	return "", fmt.Errorf("无法识别 %s，请指定表格类型", filename)
}

func detectProfitKindFromName(name string) string {
	base := filepath.Base(name)
	switch {
	case containsAny(base, "SettledParentFlow", "po聚合"):
		return ProfitFileSettledParent
	case containsAny(base, "SettledFlow", "PO明细"):
		return ProfitFileSettledFlow
	case containsAny(base, "UnsettleFlow", "待处理款项"):
		return ProfitFileUnsettle
	case containsAny(base, "FundDetail", "账务明细"):
		return ProfitFileFundDetail
	case strings.Contains(base, "发货面单费") && strings.Contains(base, "已出账"):
		return ProfitFileShippingPosted
	case strings.Contains(base, "发货面单费") && strings.Contains(base, "待出账"):
		return ProfitFileShippingPending
	case strings.Contains(base, "退至商家仓"):
		return ProfitFileReturnMerchant
	case strings.Contains(base, "退至第三方仓"):
		return ProfitFileReturnThirdParty
	default:
		return ""
	}
}

func detectProfitKindFromSheets(sheets []string) string {
	for _, name := range sheets {
		if name == "支出-买家拒付" || name == "结算" || strings.Contains(name, "履约违规") || strings.Contains(name, "退货面单费平台承担") {
			return ProfitFileFundDetail
		}
	}
	return ""
}

func detectProfitKindFromHeaders(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	switch cell(rows[0], 0) {
	case "结算流水ID":
		return ProfitFileSettledFlow
	case "PO单号":
		if len(rows) > 1 && (cell(rows[1], 3) == "SKU货号" || cell(rows[1], 5) == "是否活动价") {
			return ProfitFileSettledParent
		}
		if cell(rows[0], 0) == "PO单号" && containsAny(cell(rows[0], 6), "销售回款") {
			return ProfitFileUnsettle
		}
		if len(rows) > 0 && headerHas(rows[0], "销售回款") && !headerHas(rows[0], "是否活动价") {
			return ProfitFileUnsettle
		}
	}
	return ""
}

func parseFundDetailWorkbook(book *excelize.File, sheets []string, opts profitParseOptions, batch *ProfitImportBatch) error {
	known := 0
	for _, sheet := range sheets {
		rows, err := book.GetRows(sheet)
		if err != nil {
			return fmt.Errorf("读取工作表 %s: %w", sheet, err)
		}
		switch sheet {
		case "支出-买家拒付":
			batch.Chargebacks, err = parseBuyerChargebacks(rows, opts.ShopKey, opts.Location)
			known++
		case "支出-履约违规-1":
			batch.Violations, err = appendFulfillmentViolations(batch.Violations, rows, opts.ShopKey, ProfitViolationFulfillmentDelay, opts.Location)
			known++
		case "支出-履约违规-2":
			batch.Violations, err = appendFulfillmentViolations(batch.Violations, rows, opts.ShopKey, ProfitViolationFulfillmentFalseShip, opts.Location)
			known++
		case "其他-退货面单费平台承担":
			batch.Platforms, err = parsePlatformReturnLabelFees(rows, opts.ShopKey, opts.Location)
			known++
		case "支出-处置费":
			batch.Disposals, err = parseDisposalFees(rows, opts.ShopKey, opts.Location)
			known++
		case "结算":
			batch.Settled, err = parseSettledFlows(rows, opts.ShopKey, opts.Location)
			known++
		default:
			if !sheetIsEmpty(rows) {
				batch.Warnings = append(batch.Warnings, "跳过未识别工作表 "+sheet)
			}
		}
		if err != nil {
			return fmt.Errorf("工作表 %s: %w", sheet, err)
		}
	}
	if known == 0 {
		return fmt.Errorf("账务明细中没有可导入的工作表")
	}
	return nil
}

func parseShippingLabelFees(rows [][]string, shopKey, phase string, loc *time.Location) ([]TemuProfitShippingLabelFee, error) {
	if err := requireHeaders(rows, 0, []string{"包裹号", "运单号", "服务商code", "账单类型", "备注", "运费", "币种", "对账单状态", "支出/退款时间"}); err != nil {
		return nil, err
	}
	out := make([]TemuProfitShippingLabelFee, 0, max(0, len(rows)-1))
	for index, row := range rows[1:] {
		if rowEmpty(row) {
			continue
		}
		amount, err := parseRequiredFloat(cell(row, 5), "运费")
		if err != nil {
			return nil, rowError(index+2, err)
		}
		occurred, err := parseProfitTime(cell(row, 8), loc)
		if err != nil {
			return nil, rowError(index+2, err)
		}
		item := TemuProfitShippingLabelFee{
			ShopKey: shopKey, BillingPhase: phase,
			PackageNo: cell(row, 0), TrackingNo: cell(row, 1), CarrierCode: cell(row, 2),
			BillType: cell(row, 3), Remark: cell(row, 4), FreightAmount: amount,
			Currency: cell(row, 6), ReconcileStatus: cell(row, 7), OccurredAt: occurred,
		}
		if item.PackageNo == "" && item.TrackingNo == "" {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func parseReturnLabelFees(rows [][]string, shopKey, destination string, loc *time.Location) ([]TemuProfitReturnLabelFee, error) {
	if err := requireHeaders(rows, 0, []string{"账务单ID", "运单号", "PO单号", "账单类型", "币种", "账单备注", "金额", "账单支出/退款时间"}); err != nil {
		return nil, err
	}
	out := make([]TemuProfitReturnLabelFee, 0, max(0, len(rows)-1))
	for index, row := range rows[1:] {
		if rowEmpty(row) || cell(row, 0) == "" {
			continue
		}
		amount, err := parseRequiredFloat(cell(row, 6), "金额")
		if err != nil {
			return nil, rowError(index+2, err)
		}
		occurred, err := parseProfitTime(cell(row, 7), loc)
		if err != nil {
			return nil, rowError(index+2, err)
		}
		out = append(out, TemuProfitReturnLabelFee{
			ShopKey: shopKey, ReturnDestination: destination,
			FundBillID: cell(row, 0), TrackingNo: cell(row, 1), PONo: cell(row, 2),
			BillType: cell(row, 3), Currency: cell(row, 4), BillRemark: cell(row, 5),
			Amount: amount, OccurredAt: occurred,
		})
	}
	return out, nil
}

func parseBuyerChargebacks(rows [][]string, shopKey string, loc *time.Location) ([]TemuProfitBuyerChargeback, error) {
	if err := requireHeaders(rows, 0, []string{"违规单号", "支出金额", "币种", "账务时间"}); err != nil {
		return nil, err
	}
	out := make([]TemuProfitBuyerChargeback, 0, max(0, len(rows)-1))
	for index, row := range rows[1:] {
		if rowEmpty(row) || cell(row, 0) == "" {
			continue
		}
		amount, err := parseRequiredFloat(cell(row, 1), "支出金额")
		if err != nil {
			return nil, rowError(index+2, err)
		}
		accounted, err := parseProfitTime(cell(row, 3), loc)
		if err != nil {
			return nil, rowError(index+2, err)
		}
		out = append(out, TemuProfitBuyerChargeback{
			ShopKey: shopKey, ViolationOrderNo: cell(row, 0), Amount: amount,
			Currency: cell(row, 2), AccountedAt: accounted,
		})
	}
	return out, nil
}

func appendFulfillmentViolations(dst []TemuProfitFulfillmentViolation, rows [][]string, shopKey, sourceSheet string, loc *time.Location) ([]TemuProfitFulfillmentViolation, error) {
	if err := requireHeaders(rows, 0, []string{"违规编号", "订单编号", "违规类型", "支出金额", "币种", "账务时间"}); err != nil {
		return dst, err
	}
	for index, row := range rows[1:] {
		if rowEmpty(row) || cell(row, 0) == "" {
			continue
		}
		amount, err := parseRequiredFloat(cell(row, 3), "支出金额")
		if err != nil {
			return dst, rowError(index+2, err)
		}
		accounted, err := parseProfitTime(cell(row, 5), loc)
		if err != nil {
			return dst, rowError(index+2, err)
		}
		dst = append(dst, TemuProfitFulfillmentViolation{
			ShopKey: shopKey, SourceSheet: sourceSheet, ViolationNo: cell(row, 0),
			OrderNo: cell(row, 1), ViolationType: cell(row, 2), Amount: amount,
			Currency: cell(row, 4), AccountedAt: accounted,
		})
	}
	return dst, nil
}

func parsePlatformReturnLabelFees(rows [][]string, shopKey string, loc *time.Location) ([]TemuProfitPlatformReturnLabelFee, error) {
	if err := requireHeaders(rows, 0, []string{"订单编号", "收支金额", "币种", "备注", "账务时间"}); err != nil {
		return nil, err
	}
	out := make([]TemuProfitPlatformReturnLabelFee, 0, max(0, len(rows)-1))
	for index, row := range rows[1:] {
		if rowEmpty(row) || cell(row, 0) == "" {
			continue
		}
		amount, err := parseRequiredFloat(cell(row, 1), "收支金额")
		if err != nil {
			return nil, rowError(index+2, err)
		}
		accounted, err := parseProfitTime(cell(row, 4), loc)
		if err != nil {
			return nil, rowError(index+2, err)
		}
		out = append(out, TemuProfitPlatformReturnLabelFee{
			ShopKey: shopKey, OrderNo: cell(row, 0), Amount: amount,
			Currency: cell(row, 2), Remark: cell(row, 3), AccountedAt: accounted,
		})
	}
	return out, nil
}

func parseDisposalFees(rows [][]string, shopKey string, loc *time.Location) ([]TemuProfitDisposalFee, error) {
	if err := requireHeaders(rows, 0, []string{"SKU ID", "运单号类型", "运单号", "销毁件数", "回仓时间", "销毁费用", "币种", "账务时间"}); err != nil {
		return nil, err
	}
	out := make([]TemuProfitDisposalFee, 0, max(0, len(rows)-1))
	for index, row := range rows[1:] {
		if rowEmpty(row) || cell(row, 0) == "" {
			continue
		}
		skuID, err := parseRequiredInt64(cell(row, 0), "SKU ID")
		if err != nil {
			return nil, rowError(index+2, err)
		}
		qty, err := parseOptionalInt(cell(row, 3))
		if err != nil {
			return nil, rowError(index+2, err)
		}
		returned, err := parseProfitTime(cell(row, 4), loc)
		if err != nil {
			return nil, rowError(index+2, err)
		}
		fee, err := parseRequiredFloat(cell(row, 5), "销毁费用")
		if err != nil {
			return nil, rowError(index+2, err)
		}
		accounted, err := parseProfitTime(cell(row, 7), loc)
		if err != nil {
			return nil, rowError(index+2, err)
		}
		out = append(out, TemuProfitDisposalFee{
			ShopKey: shopKey, SKUID: skuID, TrackingType: cell(row, 1), TrackingNo: cell(row, 2),
			DestroyQty: qty, ReturnedAt: returned, DestroyFee: fee, Currency: cell(row, 6), AccountedAt: accounted,
		})
	}
	return out, nil
}

func parseSettledFlows(rows [][]string, shopKey string, loc *time.Location) ([]TemuProfitSettledFlow, error) {
	if err := requireHeaders(rows, 0, []string{"结算流水ID", "批次号", "PO单号", "单据号", "交易类型", "结算金额"}); err != nil {
		return nil, err
	}
	start := 1
	if len(rows) > 1 && cell(rows[1], 6) == "SKU ID" {
		start = 2
	}
	out := make([]TemuProfitSettledFlow, 0, max(0, len(rows)-start))
	for index, row := range rows[start:] {
		if rowEmpty(row) || cell(row, 0) == "" {
			continue
		}
		amount, err := parseRequiredFloat(cell(row, 5), "结算金额")
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		skuID, err := parseOptionalInt64(cell(row, 6))
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		qty, err := parseOptionalFloat(cell(row, 9))
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		declared, err := parseOptionalFloat(cell(row, 10))
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		activity, err := parseOptionalBool(cell(row, 11))
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		totalDeclared, err := parseOptionalFloat(cell(row, 13))
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		skuCoupon, err := parseOptionalFloatOrZero(cell(row, 14))
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		shopCoupon, err := parseOptionalFloatOrZero(cell(row, 15))
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		discount, err := parseOptionalFloatOrZero(cell(row, 16))
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		freightDiscount, err := parseOptionalFloatOrZero(cell(row, 17))
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		received, err := parseProfitTime(cell(row, 18), loc)
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		out = append(out, TemuProfitSettledFlow{
			ShopKey: shopKey, FlowID: cell(row, 0), BatchNo: cell(row, 1), PONo: cell(row, 2),
			DocumentNo: cell(row, 3), TradeType: cell(row, 4), SettleAmount: amount,
			SKUID: skuID, SKUName: cell(row, 7), SKUExtCode: cell(row, 8),
			Quantity: qty, DeclaredPrice: declared, IsActivityPrice: activity,
			Currency: cell(row, 12), TotalDeclaredPrice: totalDeclared,
			SKUCouponAmount: skuCoupon, ShopCouponAmount: shopCoupon,
			DeclaredDiscountAmount: discount, ActivityFreightDiscountAmount: freightDiscount,
			ReceivedAt: received, Remark: cell(row, 19),
		})
	}
	return out, nil
}

func parseSettledParentOrders(rows [][]string, shopKey string) ([]TemuProfitSettledParentOrder, error) {
	if err := requireHeaders(rows, 0, []string{"PO单号", "商品信息"}); err != nil {
		return nil, err
	}
	start := 1
	if len(rows) > 1 && cell(rows[1], 1) == "SKU ID" {
		start = 2
	}
	var out []TemuProfitSettledParentOrder
	var current *TemuProfitSettledParentOrder
	flush := func() {
		if current != nil {
			out = append(out, *current)
			current = nil
		}
	}
	for index, row := range rows[start:] {
		if rowEmpty(row) {
			continue
		}
		poNo := cell(row, 0)
		if poNo == "" {
			if current == nil {
				return nil, rowError(index+start+1, fmt.Errorf("缺少 PO单号"))
			}
			sku, err := parseSettledParentSKU(row, len(current.SKUs)+1)
			if err != nil {
				return nil, rowError(index+start+1, err)
			}
			current.SKUs = append(current.SKUs, sku)
			continue
		}
		flush()
		order, err := parseSettledParentOrderRow(row, shopKey)
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		current = &order
	}
	flush()
	return out, nil
}

func parseSettledParentOrderRow(row []string, shopKey string) (TemuProfitSettledParentOrder, error) {
	sales, err := parseOptionalFloatOrZero(cell(row, 8))
	if err != nil {
		return TemuProfitSettledParentOrder{}, err
	}
	salesAfter, err := parseOptionalFloatOrZero(cell(row, 9))
	if err != nil {
		return TemuProfitSettledParentOrder{}, err
	}
	salesBack, err := parseOptionalFloatOrZero(cell(row, 10))
	if err != nil {
		return TemuProfitSettledParentOrder{}, err
	}
	freight, err := parseOptionalFloatOrZero(cell(row, 11))
	if err != nil {
		return TemuProfitSettledParentOrder{}, err
	}
	freightAfter, err := parseOptionalFloatOrZero(cell(row, 12))
	if err != nil {
		return TemuProfitSettledParentOrder{}, err
	}
	freightBack, err := parseOptionalFloatOrZero(cell(row, 13))
	if err != nil {
		return TemuProfitSettledParentOrder{}, err
	}
	order := TemuProfitSettledParentOrder{
		ShopKey: shopKey, PONo: cell(row, 0), Currency: cell(row, 7),
		SalesReceipt: sales, SalesReceiptAfterDiscount: salesAfter, SalesChargeback: salesBack,
		FreightReceipt: freight, FreightReceiptAfterDiscount: freightAfter, FreightChargeback: freightBack,
	}
	if cell(row, 1) != "" {
		sku, err := parseSettledParentSKU(row, 1)
		if err != nil {
			return TemuProfitSettledParentOrder{}, err
		}
		order.SKUs = []TemuProfitSettledParentSKU{sku}
	}
	return order, nil
}

func parseSettledParentSKU(row []string, lineNo int) (TemuProfitSettledParentSKU, error) {
	skuID, err := parseRequiredInt64(cell(row, 1), "SKU ID")
	if err != nil {
		return TemuProfitSettledParentSKU{}, err
	}
	qty, err := parseRequiredFloat(cell(row, 4), "件数")
	if err != nil {
		return TemuProfitSettledParentSKU{}, err
	}
	declared, err := parseRequiredFloat(cell(row, 5), "申报价格")
	if err != nil {
		return TemuProfitSettledParentSKU{}, err
	}
	activity, err := parseOptionalBool(cell(row, 6))
	if err != nil {
		return TemuProfitSettledParentSKU{}, err
	}
	return TemuProfitSettledParentSKU{
		LineNo: lineNo, SKUID: skuID, SKUName: cell(row, 2), SKUExtCode: cell(row, 3),
		Quantity: qty, DeclaredPrice: declared, IsActivityPrice: activity,
	}, nil
}

func parseUnsettleOrders(rows [][]string, shopKey string) ([]TemuProfitUnsettleOrder, error) {
	if err := requireHeaders(rows, 0, []string{"PO单号", "商品信息"}); err != nil {
		return nil, err
	}
	start := 1
	if len(rows) > 1 && cell(rows[1], 1) == "SKU ID" {
		start = 2
	}
	var out []TemuProfitUnsettleOrder
	var current *TemuProfitUnsettleOrder
	flush := func() {
		if current != nil {
			out = append(out, *current)
			current = nil
		}
	}
	for index, row := range rows[start:] {
		if rowEmpty(row) {
			continue
		}
		poNo := cell(row, 0)
		if poNo == "" {
			if current == nil {
				return nil, rowError(index+start+1, fmt.Errorf("缺少 PO单号"))
			}
			sku, err := parseUnsettleSKU(row, len(current.SKUs)+1)
			if err != nil {
				return nil, rowError(index+start+1, err)
			}
			current.SKUs = append(current.SKUs, sku)
			continue
		}
		flush()
		order, err := parseUnsettleOrderRow(row, shopKey)
		if err != nil {
			return nil, rowError(index+start+1, err)
		}
		current = &order
	}
	flush()
	return out, nil
}

func parseUnsettleOrderRow(row []string, shopKey string) (TemuProfitUnsettleOrder, error) {
	sales, err := parseOptionalFloatOrZero(cell(row, 6))
	if err != nil {
		return TemuProfitUnsettleOrder{}, err
	}
	salesAfter, err := parseOptionalFloatOrZero(cell(row, 7))
	if err != nil {
		return TemuProfitUnsettleOrder{}, err
	}
	salesBack, err := parseOptionalFloatOrZero(cell(row, 8))
	if err != nil {
		return TemuProfitUnsettleOrder{}, err
	}
	freight, err := parseOptionalFloatOrZero(cell(row, 9))
	if err != nil {
		return TemuProfitUnsettleOrder{}, err
	}
	freightBack, err := parseOptionalFloatOrZero(cell(row, 10))
	if err != nil {
		return TemuProfitUnsettleOrder{}, err
	}
	freightAfter, err := parseOptionalFloatOrZero(cell(row, 11))
	if err != nil {
		return TemuProfitUnsettleOrder{}, err
	}
	order := TemuProfitUnsettleOrder{
		ShopKey: shopKey, PONo: cell(row, 0), Currency: cell(row, 5),
		SalesReceipt: sales, SalesReceiptAfterDiscount: salesAfter, SalesChargeback: salesBack,
		FreightReceipt: freight, FreightChargeback: freightBack, FreightReceiptAfterDiscount: freightAfter,
	}
	if cell(row, 1) != "" {
		sku, err := parseUnsettleSKU(row, 1)
		if err != nil {
			return TemuProfitUnsettleOrder{}, err
		}
		order.SKUs = []TemuProfitUnsettleSKU{sku}
	}
	return order, nil
}

func parseUnsettleSKU(row []string, lineNo int) (TemuProfitUnsettleSKU, error) {
	skuID, err := parseRequiredInt64(cell(row, 1), "SKU ID")
	if err != nil {
		return TemuProfitUnsettleSKU{}, err
	}
	qty, err := parseRequiredFloat(cell(row, 3), "件数")
	if err != nil {
		return TemuProfitUnsettleSKU{}, err
	}
	declared, err := parseRequiredFloat(cell(row, 4), "申报价格")
	if err != nil {
		return TemuProfitUnsettleSKU{}, err
	}
	return TemuProfitUnsettleSKU{LineNo: lineNo, SKUID: skuID, SKUName: cell(row, 2), Quantity: qty, DeclaredPrice: declared}, nil
}

func requireHeaders(rows [][]string, rowIdx int, prefixes []string) error {
	if len(rows) <= rowIdx {
		return fmt.Errorf("缺少表头")
	}
	for i, prefix := range prefixes {
		got := cell(rows[rowIdx], i)
		if prefix != "" && !strings.Contains(got, prefix) && got != prefix {
			return fmt.Errorf("第 %d 列表头应为 %s，实际为 %s", i+1, prefix, got)
		}
	}
	return nil
}

func parseProfitTime(raw string, loc *time.Location) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "--" || raw == "-" || strings.EqualFold(raw, "null") {
		return nil, nil
	}
	for _, layout := range profitTimeLayouts {
		if parsed, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return &parsed, nil
		}
	}
	if serial, err := strconv.ParseFloat(raw, 64); err == nil && serial > 20000 && serial < 80000 {
		base := time.Date(1899, 12, 30, 0, 0, 0, 0, loc)
		parsed := base.Add(time.Duration(serial * float64(24*time.Hour)))
		return &parsed, nil
	}
	return nil, fmt.Errorf("无法解析时间 %q", raw)
}

func parseRequiredFloat(raw, name string) (float64, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, ",", ""))
	if raw == "" {
		return 0, fmt.Errorf("%s 为空", name)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s 不是数字: %s", name, raw)
	}
	return value, nil
}

func parseOptionalFloat(raw string) (*float64, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, ",", ""))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, fmt.Errorf("不是数字: %s", raw)
	}
	return &value, nil
}

func parseOptionalFloatOrZero(raw string) (float64, error) {
	value, err := parseOptionalFloat(raw)
	if err != nil || value == nil {
		return 0, err
	}
	return *value, nil
}

func parseRequiredInt64(raw, name string) (int64, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, ",", ""))
	if raw == "" {
		return 0, fmt.Errorf("%s 为空", name)
	}
	if strings.Contains(raw, ".") {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, fmt.Errorf("%s 不是整数: %s", name, raw)
		}
		return int64(value), nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s 不是整数: %s", name, raw)
	}
	return value, nil
}

func parseOptionalInt64(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := parseRequiredInt64(raw, "数值")
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func parseOptionalInt(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := parseRequiredInt64(raw, "数值")
	return int(value), err
}

func parseOptionalBool(raw string) (*bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	switch raw {
	case "是", "Y", "y", "true", "TRUE", "1":
		value := true
		return &value, nil
	case "否", "N", "n", "false", "FALSE", "0":
		value := false
		return &value, nil
	default:
		return nil, fmt.Errorf("无法解析是否活动价 %q", raw)
	}
}

func cell(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[index])
}

func rowEmpty(row []string) bool {
	for _, value := range row {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

func sheetIsEmpty(rows [][]string) bool {
	for _, row := range rows {
		if !rowEmpty(row) {
			return false
		}
	}
	return true
}

func headerHas(row []string, part string) bool {
	for _, value := range row {
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
}

func containsAny(value string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
}

func rowError(row int, err error) error {
	return fmt.Errorf("第 %d 行: %w", row, err)
}

func (b ProfitImportBatch) recordCount() int {
	count := len(b.Shipping) + len(b.Returns) + len(b.Chargebacks) + len(b.Violations) + len(b.Platforms) + len(b.Disposals) + len(b.Settled) + len(b.Parents) + len(b.Unsettles)
	for _, item := range b.Parents {
		count += len(item.SKUs)
	}
	for _, item := range b.Unsettles {
		count += len(item.SKUs)
	}
	return count
}
