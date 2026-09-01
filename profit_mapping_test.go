package main

import (
	"reflect"
	"testing"
)

func TestTemuProfitTypesCoverExcelColumns(t *testing.T) {
	cases := []struct {
		source  string
		model   any
		columns []string
	}{
		{
			source: "发货面单费-已出账 / 发货面单费-待出账",
			model:  TemuProfitShippingLabelFee{},
			columns: []string{
				"PackageNo", "TrackingNo", "CarrierCode", "BillType", "Remark",
				"FreightAmount", "Currency", "ReconcileStatus", "OccurredAt",
			},
		},
		{
			source: "退货面单费-退至商家仓 / 退货面单费-退至第三方仓",
			model:  TemuProfitReturnLabelFee{},
			columns: []string{
				"FundBillID", "TrackingNo", "PONo", "BillType", "Currency",
				"BillRemark", "Amount", "OccurredAt",
			},
		},
		{
			source:  "对账中心-账务明细 / 支出-买家拒付",
			model:   TemuProfitBuyerChargeback{},
			columns: []string{"ViolationOrderNo", "Amount", "Currency", "AccountedAt"},
		},
		{
			source:  "对账中心-账务明细 / 支出-履约违规-1、支出-履约违规-2",
			model:   TemuProfitFulfillmentViolation{},
			columns: []string{"ViolationNo", "OrderNo", "ViolationType", "Amount", "Currency", "AccountedAt"},
		},
		{
			source:  "对账中心-账务明细 / 其他-退货面单费平台承担",
			model:   TemuProfitPlatformReturnLabelFee{},
			columns: []string{"OrderNo", "Amount", "Currency", "Remark", "AccountedAt"},
		},
		{
			source: "对账中心-账务明细 / 支出-处置费",
			model:  TemuProfitDisposalFee{},
			columns: []string{
				"SKUID", "TrackingType", "TrackingNo", "DestroyQty",
				"ReturnedAt", "DestroyFee", "Currency", "AccountedAt",
			},
		},
		{
			source: "对账中心-账务明细 / 结算 与 结算数据-已到账款项-PO明细账单",
			model:  TemuProfitSettledFlow{},
			columns: []string{
				"FlowID", "BatchNo", "PONo", "DocumentNo", "TradeType", "SettleAmount",
				"SKUID", "SKUName", "SKUExtCode", "Quantity", "DeclaredPrice", "IsActivityPrice",
				"Currency", "TotalDeclaredPrice", "SKUCouponAmount", "ShopCouponAmount",
				"DeclaredDiscountAmount", "ActivityFreightDiscountAmount", "ReceivedAt", "Remark",
			},
		},
		{
			source: "结算数据-已到账款项-po聚合账单 / PO 金额",
			model:  TemuProfitSettledParentOrder{},
			columns: []string{
				"PONo", "Currency", "SalesReceipt", "SalesReceiptAfterDiscount",
				"SalesChargeback", "FreightReceipt", "FreightReceiptAfterDiscount", "FreightChargeback",
			},
		},
		{
			source:  "结算数据-已到账款项-po聚合账单 / 商品信息",
			model:   TemuProfitSettledParentSKU{},
			columns: []string{"SKUID", "SKUName", "SKUExtCode", "Quantity", "DeclaredPrice", "IsActivityPrice"},
		},
		{
			source: "结算数据-待处理款项 / PO 金额",
			model:  TemuProfitUnsettleOrder{},
			columns: []string{
				"PONo", "Currency", "SalesReceipt", "SalesReceiptAfterDiscount",
				"SalesChargeback", "FreightReceipt", "FreightChargeback", "FreightReceiptAfterDiscount",
			},
		},
		{
			source:  "结算数据-待处理款项 / 商品信息",
			model:   TemuProfitUnsettleSKU{},
			columns: []string{"SKUID", "SKUName", "Quantity", "DeclaredPrice"},
		},
	}

	for _, tc := range cases {
		fields := structFieldSet(tc.model)
		for _, col := range tc.columns {
			if !fields[col] {
				t.Errorf("%s missing mapped field %s", tc.source, col)
			}
		}
	}
}

func structFieldSet(v any) map[string]bool {
	t := reflect.TypeOf(v)
	out := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out[t.Field(i).Name] = true
	}
	return out
}
