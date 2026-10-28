package main

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReviewMoneyUsesExactLargestRemainder(t *testing.T) {
	for _, amount := range []string{"0.01", "-0.01", "0.0001", "-1.2345678901", "999999999999999999.99"} {
		parts, err := reviewSplit(amount, []ledgerReviewPart{{PO: "P", SKU: "B", Weight: "1"}, {PO: "P", SKU: "A", Weight: "1"}, {PO: "P", SKU: "C", Weight: "1"}})
		if err != nil {
			t.Fatal(err)
		}
		sum, _ := reviewNumber("0", true)
		for _, p := range parts {
			n, e := reviewNumber(p.Amount, true)
			if e != nil {
				t.Fatal(e)
			}
			sum.Add(sum, n)
		}
		want, _ := reviewNumber(amount, true)
		if sum.Cmp(want) != 0 {
			t.Fatal(amount, parts)
		}
	}
	parts, _ := reviewSplit("-0.01", []ledgerReviewPart{{PO: "P", SKU: "B", Weight: "1"}, {PO: "P", SKU: "A", Weight: "1"}})
	if parts[1].Amount != "-0.01" {
		t.Fatal("tie order", parts)
	}
	for _, v := range []string{"NaN", "1e9", "+1", "-0.00000000001"} {
		if _, err := reviewNumber(v, true); err == nil {
			t.Fatal("invalid", v)
		}
	}
}
func reviewFixture(t *testing.T) (*APIServer, string) {
	store, ctx := ledgerFixture(t)
	// Match the ledger handlers' query settings for direct SQL assertions.
	store.db.SetMaxOpenConns(1)
	if _, err := store.db.ExecContext(ctx, `SET jit=off; SET enable_nestloop=off`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := store.db.ExecContext(ctx, `DELETE FROM profit_ledger_review_history WHERE review_id IN(SELECT id FROM profit_ledger_reviews WHERE shop_key='attribution-test');DELETE FROM profit_ledger_reviews WHERE shop_key='attribution-test'`)
		if err != nil {
			t.Error(err)
		}
	})
	server, _ := newAPIServer(store, &Syncer{}, nil, nil, "Asia/Shanghai", slog.New(slog.NewTextHandler(io.Discard, nil)))
	var entry string
	if err := store.db.QueryRowContext(ctx, `SELECT entry_key FROM profit_ledger WHERE shop_key='attribution-test' AND source_reference='BLANK'`).Scan(&entry); err != nil {
		t.Fatal(err)
	}
	return server, entry
}
func reviewRequest(s *APIServer, action string, input any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(input)
	r := httptest.NewRequest("POST", "/api/profit/ledger/review"+action, strings.NewReader(string(raw)))
	r.Header.Set("X-Pangu-Request", "finance")
	w := httptest.NewRecorder()
	switch action {
	case "/preview":
		s.ledgerReviewPreview(w, r)
	case "/withdraw":
		s.withdrawLedgerReview(w, r)
	default:
		s.saveLedgerReview(w, r)
	}
	return w
}
func readReview(t *testing.T, s *APIServer, entry string) ledgerReviewSource {
	w := httptest.NewRecorder()
	s.ledgerReviewDetail(w, httptest.NewRequest("GET", "/api/profit/ledger/review?entry_key="+entry, nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var r struct {
		Data struct{ Source ledgerReviewSource }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r.Data.Source
}
func manualReview(t *testing.T, s *APIServer, entry string) ledgerReviewInput {
	src := readReview(t, s, entry)
	return ledgerReviewInput{Entry: entry, Fingerprint: src.Fingerprint, Kind: "allocation", Method: "manual", Reference: "invoice reference", Allocations: []ledgerReviewPart{{PO: "PO_SINGLE", SKU: "A", Amount: src.Amount}}}
}
func TestManualReviewVersionsConservationAndWithdrawal(t *testing.T) {
	s, entry := reviewFixture(t)
	input := manualReview(t, s, entry)
	bad := input
	bad.Allocations = []ledgerReviewPart{{PO: "PO_SINGLE", SKU: "A", Amount: "-10"}}
	if w := reviewRequest(s, "", bad); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	bad.Allocations = []ledgerReviewPart{{PO: "PO_OTHER", SKU: "A", Amount: "10"}}
	if w := reviewRequest(s, "", bad); w.Code != 400 {
		t.Fatal("cross shop accepted", w.Body.String())
	}
	if w := reviewRequest(s, "/preview", input); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := reviewRequest(s, "", input); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := reviewRequest(s, "", input); w.Code != 409 {
		t.Fatal("version overwrite", w.Code)
	}
	var status, amount, attributed, unassigned, nonorder, rawSKU string
	err := s.store.db.QueryRow(`SELECT allocation_status,amount::text,attributed_amount::text,unattributed_amount::text,non_order_amount::text FROM profit_reviewed_ledger_for_range('2026-09-01','2026-10-01','attribution-test','','',true) WHERE entry_key=$1`, entry).Scan(&status, &amount, &attributed, &unassigned, &nonorder)
	if err != nil || status != "allocated" {
		t.Fatal(status, err)
	}
	s.store.db.QueryRow(`SELECT sku_ext_code FROM temu_profit_settled_flows WHERE shop_key='attribution-test' AND flow_id='BLANK'`).Scan(&rawSKU)
	if rawSKU != "" {
		t.Fatal("source overwritten")
	}
	var count int
	s.store.db.QueryRow(`SELECT count(*) FROM profit_reviewed_ledger_for_range('2026-09-01','2026-10-01','attribution-test','','',true) WHERE amount<>attributed_amount+unattributed_amount+non_order_amount`).Scan(&count)
	if count != 0 {
		t.Fatal("money not conserved")
	}
	if w := reviewRequest(s, "/withdraw", map[string]any{"entry_key": entry, "version": 1, "reference": "void invoice"}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	input.Version = 2
	if w := reviewRequest(s, "", input); w.Code != 200 {
		t.Fatal("cannot restore", w.Body.String())
	}
	s.store.db.QueryRow(`SELECT count(*) FROM profit_ledger_review_history h JOIN profit_ledger_reviews r ON r.id=h.review_id WHERE r.entry_key=$1`, entry).Scan(&count)
	if count != 3 {
		t.Fatal("history", count)
	}
}
func TestReviewBecomesStaleWhenSourceOrTargetChanges(t *testing.T) {
	s, entry := reviewFixture(t)
	input := manualReview(t, s, entry)
	if w := reviewRequest(s, "", input); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_, err := s.store.db.Exec(`UPDATE temu_profit_settled_flows SET settle_amount=11 WHERE shop_key='attribution-test' AND flow_id='BLANK'`)
	if err != nil {
		t.Fatal(err)
	}
	var historicalAmount, historicalCurrency string
	if err := s.store.db.QueryRow(`SELECT h.after_payload->'source_snapshot'->>'amount',h.after_payload->'source_snapshot'->>'currency' FROM profit_ledger_review_history h JOIN profit_ledger_reviews r ON r.id=h.review_id WHERE r.entry_key=$1 ORDER BY h.id DESC LIMIT 1`, entry).Scan(&historicalAmount, &historicalCurrency); err != nil || historicalAmount != "10.0000" || historicalCurrency != "USD" {
		t.Fatal("lost original source evidence", historicalAmount, historicalCurrency, err)
	}
	var status, reviewStatus string
	err = s.store.db.QueryRow(`SELECT allocation_status,review->>'status' FROM profit_reviewed_ledger_for_range('2026-09-01','2026-10-01','attribution-test','','',true) WHERE entry_key=$1`, entry).Scan(&status, &reviewStatus)
	if err != nil || status != "unattributed" || reviewStatus != "stale" {
		t.Fatal(status, reviewStatus, err)
	}
	if w := reviewRequest(s, "/preview", input); w.Code != 409 {
		t.Fatal("stale source accepted", w.Body.String())
	}
	input = manualReview(t, s, entry)
	input.Version = 1
	if w := reviewRequest(s, "", input); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.store.db.Exec(`UPDATE normalized_order_lines SET platform_sku='REMOVED' WHERE order_id IN(SELECT id FROM normalized_orders WHERE shop_key='attribution-test' AND source_order_no='PO_SINGLE')`)
	// The A SKU still has source-bill evidence for this order; remove that evidence too.
	s.store.db.Exec(`UPDATE temu_profit_settled_flows SET sku_ext_code='REMOVED',sku_id=NULL WHERE shop_key='attribution-test' AND po_no='PO_SINGLE'`)
	err = s.store.db.QueryRow(`SELECT review->>'status' FROM profit_reviewed_ledger_for_range('2026-09-01','2026-10-01','attribution-test','','',true) WHERE entry_key=$1`, entry).Scan(&reviewStatus)
	if err != nil || reviewStatus != "stale" {
		t.Fatal(reviewStatus, err)
	}
}
func TestNonOrderReviewIsSeparateFromSKUCash(t *testing.T) {
	s, entry := reviewFixture(t)
	src := readReview(t, s, entry)
	input := ledgerReviewInput{Entry: entry, Fingerprint: src.Fingerprint, Kind: "non_order", Reference: "bank non-order receipt"}
	if w := reviewRequest(s, "", input); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var status string
	var nonorder, attributed, unassigned string
	err := s.store.db.QueryRow(`SELECT allocation_status,non_order_amount::text,attributed_amount::text,unattributed_amount::text FROM profit_reviewed_ledger_for_range('2026-09-01','2026-10-01','attribution-test','','',true) WHERE entry_key=$1`, entry).Scan(&status, &nonorder, &attributed, &unassigned)
	if err != nil || status != "non_order" {
		t.Fatal(status, err)
	}

	w := httptest.NewRecorder()
	s.exportProfitLedger(w, httptest.NewRequest("GET", "/api/profit/ledger/export?start=2026-09-01&end=2026-09-30&shop_key=attribution-test&status=non_order", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	records, e := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\ufeff"))).ReadAll()
	if e != nil || len(records) != 2 || len(records[1]) != 27 || records[1][25] != nonorder || !strings.Contains(records[1][26], `"status": "active"`) {
		t.Fatal("review CSV", e, records)
	}
	var snapshotAmount string
	if err := s.store.db.QueryRow(`SELECT source_snapshot->>'amount' FROM profit_ledger_reviews WHERE entry_key=$1`, entry).Scan(&snapshotAmount); err != nil || snapshotAmount != src.Amount {
		t.Fatal("non-order source amount missing", snapshotAmount, err)
	}
	var linked string
	s.store.db.QueryRow(`SELECT entry_key FROM profit_ledger WHERE shop_key='attribution-test' AND source_reference='MULTI'`).Scan(&linked)
	src = readReview(t, s, linked)
	input.Entry = linked
	input.Fingerprint = src.Fingerprint
	if w := reviewRequest(s, "", input); w.Code != 400 {
		t.Fatal("hid order fee", w.Body.String())
	}
}
func TestReviewUsesDeclaredValueAndDetectsChangedBasis(t *testing.T) {
	s, entry := reviewFixture(t)
	var parent int64
	s.store.db.QueryRow(`INSERT INTO temu_profit_settled_parent_orders(shop_key,po_no,currency) VALUES('attribution-test','PO_MULTI','USD') RETURNING id`).Scan(&parent)
	if _, err := s.store.db.Exec(`INSERT INTO temu_profit_settled_parent_skus(parent_order_id,line_no,sku_id,sku_ext_code,quantity,declared_price) VALUES($1,1,901,'A',1,10),($1,2,902,'B',1,20)`, parent); err != nil {
		t.Fatal(err)
	}
	src := readReview(t, s, entry)
	input := ledgerReviewInput{Entry: entry, Fingerprint: src.Fingerprint, Kind: "allocation", Method: "declared_value", Reference: "invoice", Orders: []string{"PO_MULTI"}}
	if w := reviewRequest(s, "", input); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var raw []byte
	var status string
	s.store.db.QueryRow(`SELECT allocations,review->>'status' FROM profit_reviewed_ledger_for_range('2026-09-01','2026-10-01','attribution-test','','',true) WHERE entry_key=$1`, entry).Scan(&raw, &status)
	if status != "active" || !strings.Contains(string(raw), `3.33`) {
		t.Fatal(status, string(raw))
	}
	s.store.db.Exec(`UPDATE temu_profit_settled_parent_skus SET declared_price=30 WHERE parent_order_id=$1 AND sku_ext_code='B'`, parent)
	if err := s.store.db.QueryRow(`SELECT review->>'status' FROM profit_reviewed_ledger_for_range('2026-09-01','2026-10-01','attribution-test','','',true) WHERE entry_key=$1`, entry).Scan(&status); err != nil || status != "stale" {
		t.Fatal("changed basis accepted", status, err)
	}
}
func TestReviewedEarlierFeeReachesLaterOrderCohort(t *testing.T) {
	s, entry := reviewFixture(t)
	s.store.db.Exec(`UPDATE temu_profit_settled_flows SET received_at='2026-08-20' WHERE shop_key='attribution-test' AND flow_id='BLANK'`)
	input := manualReview(t, s, entry)
	if w := reviewRequest(s, "", input); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := httptest.NewRecorder()
	s.marginSource(w, httptest.NewRequest("GET", "/api/profit/margin-source?start=2026-09-01&end=2026-09-30&shop_key=attribution-test", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"entry_key":"`+entry+`"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Stale August fees must still block September orders that were reviewed against them.
	if _, err := s.store.db.Exec(`UPDATE temu_profit_settled_flows SET settle_amount=11 WHERE shop_key='attribution-test' AND flow_id='BLANK'`); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.marginSource(w, httptest.NewRequest("GET", "/api/profit/margin-source?start=2026-09-01&end=2026-09-30&shop_key=attribution-test", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"entry_key":"`+entry+`"`) || !strings.Contains(w.Body.String(), `"status":"stale"`) {
		t.Fatal("stale earlier fee disappeared", w.Code, w.Body.String())
	}
}
func TestReviewWritesNeedSameOriginFinanceHeader(t *testing.T) {
	s, _ := reviewFixture(t)
	for _, origin := range []string{"", "https://foreign.test"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "http://finance.test/api/profit/ledger/review", strings.NewReader(`{}`))
		if origin != "" {
			r.Header.Set("X-Pangu-Request", "finance")
			r.Header.Set("Origin", origin)
		}
		s.saveLedgerReview(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
}
