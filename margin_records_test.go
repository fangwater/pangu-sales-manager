package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMarginEvidenceBusinessValidationAndCSV(t *testing.T) {
	valid := marginRecord{Kind: "adjustment", SourceKey: "TAX", Shop: "A", Category: "tax_withheld", Currency: "CNY", Amount: "0.3000000003", Date: "2026-09-01", Reference: "bill"}
	if _, err := valid.key(); err != nil {
		t.Fatal(err)
	}
	for _, amount := range []string{"NaN", "-1", "1e4", "1.00000000001", "1000000000000000000"} {
		r := valid
		r.Amount = amount
		if _, err := r.key(); err == nil {
			t.Fatalf("accepted %s", amount)
		}
	}
	rows, err := marginCSV("adjustment", "业务键,店铺,款项类别,币种,金额,费用日期,来源凭证\nTAX,A,税金代扣,CNY,0.3,2026-09-01,bill\n")
	if err != nil || len(rows) != 1 || rows[0].Category != "tax_withheld" {
		t.Fatalf("CSV %v %v", rows, err)
	}
}
func TestMarginEvidenceVersionsAtomicityAndNoSourceOverwrite(t *testing.T) {
	store, ctx, _, _ := attributionFixture(t)
	server, _ := newAPIServer(store, &Syncer{}, nil, nil, "Asia/Shanghai", slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() {
		store.db.ExecContext(ctx, `DELETE FROM profit_accounting_history WHERE record_id IN(SELECT id FROM profit_accounting_records WHERE payload->>'shop_key'='margin-test'); DELETE FROM profit_accounting_records WHERE payload->>'shop_key'='margin-test'`)
	})
	post := func(body string, header bool) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/profit/accounting-records", strings.NewReader(body))
		if header {
			r.Header.Set("X-Pangu-Request", "finance")
		}
		server.saveMarginRecords(w, r)
		return w
	}
	body := `{"rows":[{"kind":"adjustment","version":0,"shop_key":"margin-test","source_key":"TAX","category":"tax_withheld","currency":"CNY","amount":"0.3000000003","date":"2026-09-01","reference":"bill"}]}`
	if w := post(body, false); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := post(body, true); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := post(body, true); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := httptest.NewRecorder()
	server.marginRecords(w, httptest.NewRequest("GET", "/api/profit/accounting-records", nil))
	if !strings.Contains(w.Body.String(), `"version":1`) || !strings.Contains(w.Body.String(), `"0.3000000003"`) {
		t.Fatal(w.Body.String())
	}
	bad := strings.Replace(body, `"TAX"`, `"NEW"`, 1)
	bad = strings.TrimSuffix(bad, "]}") + `,{"kind":"adjustment","version":0,"reference":"bad"}]}`
	if w := post(bad, true); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	var count int
	store.db.QueryRowContext(ctx, `SELECT count(*) FROM profit_accounting_records WHERE payload->>'source_key'='NEW' AND payload->>'shop_key'='margin-test'`).Scan(&count)
	if count != 0 {
		t.Fatal("partial batch committed")
	}
}
func TestMarginSourcePreservesMoneyMetadataAndAllCashForCohort(t *testing.T) {
	store, ctx, _, _ := attributionFixture(t)
	_, err := store.db.ExecContext(ctx, `UPDATE normalized_orders SET raw_payload=raw_payload||'{"parentOrderMap":{"siteId":100},"buyer":"PRIVATE"}'::jsonb WHERE shop_key='attribution-test'; UPDATE normalized_order_lines SET raw_payload=raw_payload||'{"goodsId":123456789012345678}'::jsonb WHERE order_id IN(SELECT id FROM normalized_orders WHERE shop_key='attribution-test')`)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := newAPIServer(store, &Syncer{}, nil, nil, "Asia/Shanghai", slog.New(slog.NewTextHandler(io.Discard, nil)))
	w := httptest.NewRecorder()
	server.marginSource(w, httptest.NewRequest("GET", "/api/profit/margin-source?start=2026-09-01&end=2026-09-30&shop_key=attribution-test", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Items []map[string]any
			Cash  []map[string]any
			SHA   string `json:"sha256"`
		}
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Items) == 0 || len(response.Data.Cash) == 0 || len(response.Data.SHA) != 64 {
		t.Fatal(w.Body.String())
	}
	if response.Data.Items[0]["product_id"] != "123456789012345678" || response.Data.Items[0]["site"] != "100" {
		t.Fatal("metadata missing")
	}
	if strings.Contains(w.Body.String(), "PRIVATE") {
		t.Fatal("buyer leaked")
	}
	for _, row := range response.Data.Cash {
		if _, ok := row["amount"].(string); !ok {
			t.Fatal("money must be decimal string")
		}
	}
}
func TestMarginSnapshotIsCompleteAndImmutableThroughAPI(t *testing.T) {
	store, ctx, _, _ := attributionFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "1" {
			t.Error("snapshot omitted rows beyond page")
		}
		w.Write([]byte(`{"success":true,"data":{"status":"review","summary":{"sku_rows":51},"rows":[],"range":{"start":"2026-09-01","end":"2026-09-30"}}}`))
	}))
	defer upstream.Close()
	t.Setenv("RESTOCK_API_BASE_URL", upstream.URL)
	server, _ := newAPIServer(store, &Syncer{}, nil, nil, "Asia/Shanghai", slog.New(slog.NewTextHandler(io.Discard, nil)))
	var identity int64
	t.Cleanup(func() { store.db.ExecContext(ctx, `DELETE FROM profit_margin_reports WHERE id=$1`, identity) })
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/profit/margin/snapshots", strings.NewReader(`{"start":"2026-09-01","end":"2026-09-30","as_of":"2026-09-30"}`))
		r.Header.Set("X-Pangu-Request", "finance")
		server.saveMarginSnapshot(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var response struct{ Data struct{ ID int64 } }
		json.Unmarshal(w.Body.Bytes(), &response)
		if i == 0 {
			identity = response.Data.ID
		} else if identity != response.Data.ID {
			t.Fatal("identical report duplicated")
		}
	}
	var payload, hash string
	if err := store.db.QueryRowContext(ctx, `SELECT payload,sha256 FROM profit_margin_reports WHERE id=$1`, identity).Scan(&payload, &hash); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"sku_rows":51`) || len(hash) != 64 {
		t.Fatal("snapshot altered")
	}
}

func TestMarginPaymentTimestampCSVHasExplicitTimezone(t *testing.T) {
	rows, err := marginCSV("metadata", "流水键,付款时间（北京时间）,来源凭证\nflow:1,2026-09-21 12:30:15,bill\n")
	if err != nil || len(rows) != 1 || rows[0].PaidAt != "2026-09-21T12:30:15+08:00" {
		t.Fatal(rows, err)
	}
	if _, err = rows[0].key(); err != nil {
		t.Fatal(err)
	}
	rows[0].PaidAt = "2026-09-21T12:30:15"
	if _, err = rows[0].key(); err == nil {
		t.Fatal("timezone omitted")
	}
}

func TestMarginSnapshotExportsCashEvidenceWithoutDroppingUnassigned(t *testing.T) {
	payload := []byte(`{"rows":[],"unassigned":[{"entry_key":"=UNTRUSTED","shop_key":"A","amount":"-1.2500","currency":"USD","allocation_reason":"missing SKU"}],"overhead":[{"entry_key":"supplement:1","amount":"-2.30","currency":"CNY"}]}`)
	for _, part := range []string{"unassigned", "overhead"} {
		w := httptest.NewRecorder()
		exportMarginSnapshot(w, payload, "archive-hash", part)
		if !strings.Contains(w.Body.String(), "archive-hash") || strings.Count(w.Body.String(), "\n") != 2 {
			t.Fatal(w.Body.String())
		}
		if part == "unassigned" && (!strings.Contains(w.Body.String(), "'=UNTRUSTED") || !strings.Contains(w.Body.String(), "-1.2500")) {
			t.Fatal("unsafe or changed cash evidence", w.Body.String())
		}
	}
}

func TestDeletedMarginEvidenceCanBeRestoredAndOldVersionRejected(t *testing.T) {
	store, ctx, _, _ := attributionFixture(t)
	r := marginRecord{Kind: "adjustment", SourceKey: "RESTORE", Shop: "margin-restore-test", Category: "tax_withheld", Currency: "CNY", Amount: "1", Date: "2026-09-01", Reference: "bill"}
	t.Cleanup(func() {
		store.db.ExecContext(ctx, `DELETE FROM profit_accounting_history WHERE record_id IN(SELECT id FROM profit_accounting_records WHERE payload->>'shop_key'='margin-restore-test');DELETE FROM profit_accounting_records WHERE payload->>'shop_key'='margin-restore-test'`)
	})
	tx, _ := store.db.BeginTx(ctx, nil)
	id, _, err := saveMarginRecord(ctx, tx, r, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store.db.ExecContext(ctx, `UPDATE profit_accounting_records SET deleted_at=now(),version=version+1 WHERE id=$1`, id)
	tx, _ = store.db.BeginTx(ctx, nil)
	restored, _, err := saveMarginRecord(ctx, tx, r, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if restored != id {
		t.Fatal("changed evidence identity")
	}
	r.ID = id
	r.Version = 1
	tx, _ = store.db.BeginTx(ctx, nil)
	defer tx.Rollback()
	if _, _, err = saveMarginRecord(ctx, tx, r, false); err != errMarginConflict {
		t.Fatal("accepted stale version", err)
	}
}
