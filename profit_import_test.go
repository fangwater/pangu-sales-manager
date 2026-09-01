package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

func TestDetectProfitKindFromName(t *testing.T) {
	cases := map[string]string{
		"发货面单费-已出账95a3.xlsx":                      ProfitFileShippingPosted,
		"发货面单费-待出账bf9d.xlsx":                      ProfitFileShippingPending,
		"退货面单费-退至商家仓29b4.xlsx":                    ProfitFileReturnMerchant,
		"退货面单费-退至第三方仓2040.xlsx":                   ProfitFileReturnThirdParty,
		"对账中心-账务明细FundDetail-65bf.xlsx":           ProfitFileFundDetail,
		"结算数据-已到账款项-PO明细账单SettledFlow.xlsx":       ProfitFileSettledFlow,
		"结算数据-已到账款项-po聚合账单SettledParentFlow.xlsx": ProfitFileSettledParent,
		"结算数据-待处理款项UnsettleFlow.xlsx":             ProfitFileUnsettle,
	}
	for name, want := range cases {
		if got := detectProfitKindFromName(name); got != want {
			t.Fatalf("%s: got %s want %s", name, got, want)
		}
	}
}

func TestSkipProfitZipEntry(t *testing.T) {
	if !skipProfitZipEntry("__MACOSX/TEMU利润计算/._发货面单费.xlsx") {
		t.Fatal("macosx sidecar should be skipped")
	}
	if skipProfitZipEntry("TEMU利润计算/发货面单费-已出账.xlsx") {
		t.Fatal("xlsx should be imported")
	}
}

func TestParseShippingAndParentWorkbooks(t *testing.T) {
	loc := time.FixedZone("CST", 8*60*60)
	opts := profitParseOptions{ShopKey: "panda-homes", Location: loc}

	shipping := mustParseWorkbook(t, "发货面单费-已出账.xlsx", buildShippingWorkbook(t), opts)
	if len(shipping.Shipping) != 2 {
		t.Fatalf("shipping rows = %d", len(shipping.Shipping))
	}
	if shipping.Shipping[0].BillingPhase != ProfitBillingPosted || shipping.Shipping[1].OccurredAt != nil {
		t.Fatalf("unexpected shipping parse: %+v", shipping.Shipping)
	}
	if shipping.Shipping[0].FreightAmount != -5 {
		t.Fatalf("freight = %v", shipping.Shipping[0].FreightAmount)
	}

	parent := mustParseWorkbook(t, "结算数据-已到账款项-po聚合账单.xlsx", buildParentWorkbook(t), opts)
	if len(parent.Parents) != 1 || len(parent.Parents[0].SKUs) != 2 {
		t.Fatalf("parent = %+v", parent.Parents)
	}
	if parent.Parents[0].PONo != "PO-1" || parent.Parents[0].SKUs[1].SKUID != 200 || parent.Parents[0].SalesReceipt != 20 {
		t.Fatalf("merged parent not carried forward: %+v", parent.Parents[0])
	}

	unsettle := mustParseWorkbook(t, "结算数据-待处理款项.xlsx", buildUnsettleWorkbook(t), opts)
	if len(unsettle.Unsettles) != 1 || len(unsettle.Unsettles[0].SKUs) != 2 {
		t.Fatalf("unsettle = %+v", unsettle.Unsettles)
	}

	settled := mustParseWorkbook(t, "结算数据-已到账款项-PO明细账单.xlsx", buildSettledWorkbook(t), opts)
	if len(settled.Settled) != 2 || settled.Settled[1].SKUID != nil {
		t.Fatalf("settled freight row should have empty sku: %+v", settled.Settled)
	}
}

func TestParseProfitZipSkipsMacSidecars(t *testing.T) {
	xlsx := buildShippingWorkbook(t)
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	addZipFile(t, writer, "TEMU利润计算/发货面单费-待出账.xlsx", xlsx)
	addZipFile(t, writer, "__MACOSX/TEMU利润计算/._发货面单费-待出账.xlsx", []byte("not-xlsx"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	kind, batches, err := parseProfitUpload("TEMU利润计算.zip", buf.Bytes(), profitParseOptions{ShopKey: "panda-homes"})
	if err != nil {
		t.Fatal(err)
	}
	if kind != "zip" || len(batches) != 1 || batches[0].Kind != ProfitFileShippingPending {
		t.Fatalf("kind=%s batches=%+v", kind, batches)
	}
}

func TestParseRealTemuProfitFolder(t *testing.T) {
	root := filepath.Join("TEMU利润计算")
	if _, err := os.Stat(root); err != nil {
		t.Skip("sample TEMU folder is not present")
	}
	opts := profitParseOptions{ShopKey: "panda-homes", Location: time.FixedZone("CST", 8*60*60)}
	cases := []struct {
		match string
		kind  string
		check func(*testing.T, ProfitImportBatch)
	}{
		{"发货面单费-已出账", ProfitFileShippingPosted, func(t *testing.T, batch ProfitImportBatch) {
			if len(batch.Shipping) < 8000 {
				t.Fatalf("posted shipping rows = %d", len(batch.Shipping))
			}
		}},
		{"po聚合账单", ProfitFileSettledParent, func(t *testing.T, batch ProfitImportBatch) {
			if len(batch.Parents) < 3000 {
				t.Fatalf("parent orders = %d", len(batch.Parents))
			}
			multi := 0
			for _, item := range batch.Parents {
				if len(item.SKUs) > 1 {
					multi++
				}
			}
			if multi == 0 {
				t.Fatal("expected merged continuation SKUs")
			}
		}},
		{"待处理款项", ProfitFileUnsettle, func(t *testing.T, batch ProfitImportBatch) {
			if len(batch.Unsettles) < 2000 {
				t.Fatalf("unsettle orders = %d", len(batch.Unsettles))
			}
		}},
		{"账务明细", ProfitFileFundDetail, func(t *testing.T, batch ProfitImportBatch) {
			if len(batch.Chargebacks) < 1 || len(batch.Violations) < 100 || len(batch.Settled) < 10000 {
				t.Fatalf("fund detail chargebacks=%d violations=%d settled=%d disposals=%d platforms=%d",
					len(batch.Chargebacks), len(batch.Violations), len(batch.Settled), len(batch.Disposals), len(batch.Platforms))
			}
		}},
		{"退至商家仓", ProfitFileReturnMerchant, func(t *testing.T, batch ProfitImportBatch) {
			if len(batch.Returns) < 20 {
				t.Fatalf("merchant returns = %d", len(batch.Returns))
			}
		}},
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		var path string
		for _, entry := range entries {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".xlsx" && containsAny(entry.Name(), tc.match) {
				path = filepath.Join(root, entry.Name())
				break
			}
		}
		if path == "" {
			t.Fatalf("missing sample for %s", tc.match)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		batch, err := parseProfitWorkbook(filepath.Base(path), data, opts)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if batch.Kind != tc.kind {
			t.Fatalf("%s kind = %s", path, batch.Kind)
		}
		tc.check(t, batch)
	}
}

func mustParseWorkbook(t *testing.T, name string, data []byte, opts profitParseOptions) ProfitImportBatch {
	t.Helper()
	batch, err := parseProfitWorkbook(name, data, opts)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func buildShippingWorkbook(t *testing.T) []byte {
	t.Helper()
	book := excelize.NewFile()
	sheet := book.GetSheetName(0)
	headers := []string{"包裹号", "运单号", "服务商code", "账单类型", "备注", "运费(单位元)-支出为负、退款为正", "币种", "对账单状态", "支出/退款时间(时区：GMT+8)"}
	writeRow(t, book, sheet, 1, headers)
	writeRow(t, book, sheet, 2, []string{"PK-1", "TRK-1", "SPEx", "支出", "发货面单费（全段）", "-5.00", "USD", "支出成功", "2026-08-30 04:20:11.004"})
	writeRow(t, book, sheet, 3, []string{"PK-2", "TRK-2", "GOFo", "支出", "", "-4.33", "USD", "支出待完结", "--"})
	return workbookBytes(t, book)
}

func buildParentWorkbook(t *testing.T) []byte {
	t.Helper()
	book := excelize.NewFile()
	sheet := book.GetSheetName(0)
	writeRow(t, book, sheet, 1, []string{"PO单号", "商品信息 * 销售件数", "", "", "", "", "", "币种", "销售回款", "销售回款已减优惠", "销售冲回", "运费回款", "运费回款已减优惠", "运费冲回"})
	writeRow(t, book, sheet, 2, []string{"", "SKU ID", "SKU名称", "SKU货号", "件数", "申报价格", "是否活动价"})
	writeRow(t, book, sheet, 3, []string{"PO-1", "100", "Hang A", "A-1", "1", "10", "是", "USD", "20", "0", "0", "2.99", "0", "0"})
	writeRow(t, book, sheet, 4, []string{"", "200", "Hang B", "B-1", "1", "10", "是"})
	return workbookBytes(t, book)
}

func buildUnsettleWorkbook(t *testing.T) []byte {
	t.Helper()
	book := excelize.NewFile()
	sheet := book.GetSheetName(0)
	writeRow(t, book, sheet, 1, []string{"PO单号", "商品信息 * 销售件数", "", "", "", "币种", "销售回款", "销售回款已减优惠", "销售冲回", "运费回款", "运费冲回", "运费回款已减优惠"})
	writeRow(t, book, sheet, 2, []string{"", "SKU ID", "SKU名称", "件数", "申报价格"})
	writeRow(t, book, sheet, 3, []string{"PO-2", "300", "Hang C", "1", "6.75", "USD", "12", "0", "0", "2.99", "0", "0"})
	writeRow(t, book, sheet, 4, []string{"", "400", "Hang D", "1", "5.22"})
	return workbookBytes(t, book)
}

func buildSettledWorkbook(t *testing.T) []byte {
	t.Helper()
	book := excelize.NewFile()
	sheet := book.GetSheetName(0)
	writeRow(t, book, sheet, 1, []string{"结算流水ID", "批次号", "PO单号", "单据号", "交易类型", "结算金额", "商品信息 * 销售件数", "", "", "", "", "", "币种", "总申报价格", "单品优惠券金额", "店铺满减券优惠金额", "申报价格折扣金额", "活动运费主动减免金额", "到账时间", "备注"})
	writeRow(t, book, sheet, 2, []string{"", "", "", "", "", "", "SKU ID", "SKU名称", "SKU货号", "件数", "申报价格", "是否活动价"})
	writeRow(t, book, sheet, 3, []string{"F-1", "BATCH-1", "PO-3", "D-1", "销售回款", "11.06", "10856989348", "Hang", "VH-1", "1", "11.06", "是", "USD", "11.06", "0.00", "0.00", "0.00", "0.00", "2026-07-31 07:36:10", ""})
	writeRow(t, book, sheet, 4, []string{"F-2", "BATCH-1", "PO-3", "D-1", "运费回款", "2.99", "", "", "", "", "", "", "USD", "2.99", "0.00", "0.00", "0.00", "0.00", "2026-07-31 07:36:10", ""})
	return workbookBytes(t, book)
}

func writeRow(t *testing.T, book *excelize.File, sheet string, row int, values []string) {
	t.Helper()
	for index, value := range values {
		cellName, err := excelize.CoordinatesToCellName(index+1, row)
		if err != nil {
			t.Fatal(err)
		}
		if err := book.SetCellValue(sheet, cellName, value); err != nil {
			t.Fatal(err)
		}
	}
}

func workbookBytes(t *testing.T, book *excelize.File) []byte {
	t.Helper()
	buffer, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func addZipFile(t *testing.T, writer *zip.Writer, name string, data []byte) {
	t.Helper()
	file, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
}
