package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lib/pq"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"
)

type ledgerReviewInput struct {
	Entry       string             `json:"entry_key"`
	Version     int                `json:"version"`
	Fingerprint string             `json:"source_fingerprint"`
	Kind        string             `json:"kind"`
	Method      string             `json:"method"`
	Reference   string             `json:"reference"`
	Orders      []string           `json:"orders"`
	Allocations []ledgerReviewPart `json:"allocations"`
}
type ledgerReviewPart struct {
	PO                 string          `json:"po_no"`
	SKU                string          `json:"platform_sku"`
	Amount             string          `json:"amount"`
	Name               string          `json:"sku_name,omitempty"`
	Weight             string          `json:"weight,omitempty"`
	TotalWeight        string          `json:"total_weight,omitempty"`
	Ratio              string          `json:"ratio,omitempty"`
	Method             string          `json:"method,omitempty"`
	BasisSource        string          `json:"basis_source,omitempty"`
	BasisCurrency      string          `json:"basis_currency,omitempty"`
	Quantity           string          `json:"quantity,omitempty"`
	DeclaredValue      string          `json:"declared_value,omitempty"`
	PolicyVersion      int             `json:"policy_version,omitempty"`
	RoundingAdjustment string          `json:"rounding_adjustment,omitempty"`
	BasisEvidence      json.RawMessage `json:"basis_evidence,omitempty"`
}
type ledgerReviewSource struct {
	OccurredAt *time.Time `json:"occurred_at"`
	PaidAt     *time.Time `json:"paid_at"`
	Package    string     `json:"package_no"`
	Tracking   string     `json:"tracking_no"`
	RawSKU     string     `json:"source_sku"`

	Entry          string   `json:"entry_key"`
	Shop           string   `json:"shop_key"`
	Currency       string   `json:"currency"`
	Amount         string   `json:"amount"`
	Fingerprint    string   `json:"source_fingerprint"`
	SKUStatus      string   `json:"sku_status"`
	Source         string   `json:"source"`
	Fee            string   `json:"fee_name"`
	Reference      string   `json:"source_reference"`
	OrderReference string   `json:"order_reference"`
	Candidates     []string `json:"candidate_pos"`
}
type ledgerReviewProblem struct {
	message string
	status  int
}

func (e ledgerReviewProblem) Error() string          { return e.message }
func reviewProblem(status int, message string) error { return ledgerReviewProblem{message, status} }
func reviewFail(s *APIServer, w http.ResponseWriter, err error) {
	var p ledgerReviewProblem
	if errors.As(err, &p) {
		writeJSON(w, p.status, apiResponse{Success: false, Error: p.message})
	} else {
		s.internalError(w, "ledger review", err)
	}
}
func reviewTx(ctx context.Context, db *sql.DB, write bool) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !write, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL jit=off; SET LOCAL enable_nestloop=off`); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func loadReviewSource(ctx context.Context, tx *sql.Tx, entry string) (ledgerReviewSource, error) {
	var src ledgerReviewSource
	var raw []byte
	if entry == "" || len([]rune(entry)) > 200 {
		return src, reviewProblem(400, "流水键无效")
	}
	err := tx.QueryRowContext(ctx, `SELECT to_jsonb(l)||jsonb_build_object('amount',l.amount::text,'source_fingerprint',profit_ledger_fingerprint(to_jsonb(l))) FROM profit_ledger l WHERE entry_key=$1`, entry).Scan(&raw)
	if err == sql.ErrNoRows {
		return src, reviewProblem(404, "原始流水不存在")
	}
	if err != nil {
		return src, err
	}
	err = json.Unmarshal(raw, &src)
	return src, err
}
func reviewCurrent(ctx context.Context, tx *sql.Tx, entry string) (json.RawMessage, int, error) {
	var raw []byte
	var version int
	err := tx.QueryRowContext(ctx, `SELECT to_jsonb(r),version FROM profit_ledger_reviews r WHERE entry_key=$1`, entry).Scan(&raw, &version)
	if err == sql.ErrNoRows {
		return json.RawMessage(`null`), 0, nil
	}
	return json.RawMessage(raw), version, err
}
func reviewNumber(raw string, signed bool) (*big.Rat, error) {
	value := raw
	if signed {
		value = strings.TrimPrefix(value, "-")
	}
	if !marginNumber.MatchString(value) {
		return nil, reviewProblem(400, "金额或权重须为最多十位小数的普通数字")
	}
	n, ok := new(big.Rat).SetString(raw)
	if !ok {
		return nil, reviewProblem(400, "金额无效")
	}
	return n, nil
}
func reviewSplit(amount string, parts []ledgerReviewPart) ([]ledgerReviewPart, error) {
	total, err := reviewNumber(amount, true)
	if err != nil {
		return nil, err
	}
	sum := new(big.Rat)
	weights := make([]*big.Rat, len(parts))
	for i, p := range parts {
		weights[i], err = reviewNumber(p.Weight, false)
		if err != nil || weights[i].Sign() <= 0 {
			return nil, reviewProblem(400, "申报金额须为正数")
		}
		sum.Add(sum, weights[i])
	}
	if sum.Sign() <= 0 {
		return nil, reviewProblem(400, "缺少完整的申报金额")
	}
	precision := 2
	if dot := strings.IndexByte(amount, '.'); dot >= 0 {
		scale := len(strings.TrimRight(amount[dot+1:], "0"))
		if scale > precision {
			precision = scale
		}
	}
	quantum := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(precision)), nil)
	absolute := new(big.Rat).Abs(total)
	units := new(big.Rat).Mul(absolute, new(big.Rat).SetInt(quantum))
	if !units.IsInt() {
		return nil, reviewProblem(400, "原始金额精度无效")
	}
	floors := make([]*big.Int, len(parts))
	remainders := make([]*big.Rat, len(parts))
	used := new(big.Int)
	order := make([]int, len(parts))
	for i, weight := range weights {
		exact := new(big.Rat).Mul(units, weight)
		exact.Quo(exact, sum)
		floors[i] = new(big.Int).Quo(exact.Num(), exact.Denom())
		used.Add(used, floors[i])
		remainders[i] = new(big.Rat).Sub(exact, new(big.Rat).SetInt(floors[i]))
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if c := remainders[a].Cmp(remainders[b]); c != 0 {
			return c > 0
		}
		return parts[a].PO+"\x00"+parts[a].SKU < parts[b].PO+"\x00"+parts[b].SKU
	})
	remaining := new(big.Int).Sub(units.Num(), used)
	if !remaining.IsInt64() || remaining.Int64() > int64(len(parts)) {
		return nil, reviewProblem(400, "分摊尾差无效")
	}
	rounded := map[int]bool{}
	for _, i := range order[:int(remaining.Int64())] {
		floors[i].Add(floors[i], big.NewInt(1))
		rounded[i] = true
	}
	for i := range parts {
		n := new(big.Rat).SetFrac(floors[i], quantum)
		if total.Sign() < 0 {
			n.Neg(n)
		}
		parts[i].Amount = n.FloatString(precision)
		parts[i].TotalWeight = sum.FloatString(10)
		parts[i].Ratio = new(big.Rat).Quo(weights[i], sum).FloatString(10)
		parts[i].RoundingAdjustment = "0"
		if rounded[i] {
			d := new(big.Rat).SetFrac(big.NewInt(int64(total.Sign())), quantum)
			parts[i].RoundingAdjustment = d.FloatString(precision)
		}
	}
	return parts, nil
}
func prepareLedgerReview(ctx context.Context, tx *sql.Tx, input ledgerReviewInput) (ledgerReviewSource, []ledgerReviewPart, error) {
	src, err := loadReviewSource(ctx, tx, input.Entry)
	if err != nil {
		return src, nil, err
	}
	if src.Fingerprint != input.Fingerprint {
		return src, nil, reviewProblem(409, "原始账单或关联依据已变化，请重新读取")
	}
	if src.Currency == "" {
		return src, nil, reviewProblem(400, "原流水缺少币种，不能核对")
	}
	if src.SKUStatus == "matched" {
		return src, nil, reviewProblem(400, "原流水已唯一归因；如有过期人工核对，请撤销后使用当前结果")
	}
	if len([]rune(strings.TrimSpace(input.Reference))) < 1 || len([]rune(input.Reference)) > 1000 {
		return src, nil, reviewProblem(400, "须填写不超过 1000 字的核对凭证")
	}
	if input.Kind == "non_order" {
		if src.OrderReference != "" || len(src.Candidates) > 0 {
			return src, nil, reviewProblem(400, "原流水存在订单关联依据，不能确认为非订单款项")
		}
		return src, []ledgerReviewPart{}, nil
	}
	if input.Kind != "allocation" || !map[string]bool{"declared_value": true, "manual": true}[input.Method] {
		return src, nil, reviewProblem(400, "核对类型或分摊方式无效")
	}
	parts := []ledgerReviewPart{}
	orders := map[string]bool{}
	if input.Method == "declared_value" {
		for _, po := range input.Orders {
			if po == "" || orders[po] {
				return src, nil, reviewProblem(400, "订单号为空或重复")
			}
			orders[po] = true
		}
	}
	if input.Method == "manual" {
		for _, p := range input.Allocations {
			orders[p.PO] = true
		}
	}
	if len(orders) < 1 || len(orders) > 100 {
		return src, nil, reviewProblem(400, "须选择 1–100 个订单")
	}
	orderList := []string{}
	for po := range orders {
		if po == "" || len([]rune(po)) > 200 {
			return src, nil, reviewProblem(400, "订单号无效")
		}
		orderList = append(orderList, po)
	}
	sort.Strings(orderList)
	if input.Method == "declared_value" {
		rows, e := tx.QueryContext(ctx, `SELECT po_no,jsonb_build_object('currency',currency,'items',items,'basis_source',basis_source) FROM profit_allocation_basis WHERE shop_key=$1 AND po_no=ANY($2) ORDER BY po_no`, src.Shop, pq.Array(orderList))
		if e != nil {
			return src, nil, e
		}
		seen := map[string]bool{}
		currency := ""
		for rows.Next() {
			var po string
			var raw []byte
			if err = rows.Scan(&po, &raw); err != nil {
				rows.Close()
				return src, nil, err
			}
			var b struct {
				Currency string
				Source   string `json:"basis_source"`
				Items    []struct {
					SKU      string `json:"platform_sku"`
					Name     string `json:"sku_name"`
					Quantity json.Number
					Value    json.Number `json:"declared_value"`
				}
			}
			if err = json.Unmarshal(raw, &b); err != nil {
				rows.Close()
				return src, nil, err
			}
			if currency != "" && b.Currency != currency {
				rows.Close()
				return src, nil, reviewProblem(400, "订单申报币种不一致，不能按申报金额合并分摊")
			}
			currency = b.Currency
			seen[po] = true
			for _, item := range b.Items {
				parts = append(parts, ledgerReviewPart{PO: po, SKU: item.SKU, Name: item.Name, Weight: item.Value.String(), Quantity: item.Quantity.String(), DeclaredValue: item.Value.String(), BasisCurrency: b.Currency, BasisEvidence: json.RawMessage(raw), Method: "declared_value", BasisSource: "manual_review:" + b.Source})
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return src, nil, err
		}
		if len(seen) != len(orders) {
			return src, nil, reviewProblem(400, "所选订单缺少完整、同币种的商品申报依据；可补齐账单或使用人工金额")
		}
		parts, err = reviewSplit(src.Amount, parts)
		if err != nil {
			return src, nil, err
		}
	} else {
		if len(input.Allocations) < 1 || len(input.Allocations) > 500 {
			return src, nil, reviewProblem(400, "须填写 1–500 个分摊商品")
		}
		rows, e := tx.QueryContext(ctx, `SELECT po_no,products FROM profit_ledger_products WHERE shop_key=$1 AND po_no=ANY($2)`, src.Shop, pq.Array(orderList))
		if e != nil {
			return src, nil, e
		}
		known := map[string]string{}
		for rows.Next() {
			var po string
			var raw []byte
			if err = rows.Scan(&po, &raw); err != nil {
				rows.Close()
				return src, nil, err
			}
			var products []struct {
				SKU  string `json:"platform_sku"`
				Name string `json:"product_name"`
			}
			if err = json.Unmarshal(raw, &products); err != nil {
				rows.Close()
				return src, nil, err
			}
			for _, p := range products {
				if p.SKU != "" {
					known[po+"\x00"+p.SKU] = p.Name
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return src, nil, err
		}
		seen := map[string]bool{}
		sum := new(big.Rat)
		original, e := reviewNumber(src.Amount, true)
		if e != nil {
			return src, nil, e
		}
		for _, p := range input.Allocations {
			key := p.PO + "\x00" + p.SKU
			name, ok := known[key]
			if !ok || seen[key] || p.SKU == "" {
				return src, nil, reviewProblem(400, "分摊商品不属于原店铺所选订单，或商品重复")
			}
			seen[key] = true
			n, e := reviewNumber(p.Amount, true)
			if e != nil {
				return src, nil, e
			}
			if n.Sign() != 0 && n.Sign() != original.Sign() {
				return src, nil, reviewProblem(400, "分摊金额必须与原流水同方向，不得用正负金额相抵")
			}
			sum.Add(sum, n)
			parts = append(parts, ledgerReviewPart{PO: p.PO, SKU: p.SKU, Name: name, Amount: n.FloatString(10), Method: "manual", BasisSource: "manual_review", RoundingAdjustment: "0"})
		}
		if sum.Cmp(original) != 0 {
			return src, nil, reviewProblem(400, "分摊金额合计必须精确等于原流水金额")
		}
	}
	if len(parts) > 500 {
		return src, nil, reviewProblem(400, "分摊超过 500 个商品，请分开核对原始流水")
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PO+"\x00"+parts[i].SKU < parts[j].PO+"\x00"+parts[j].SKU })
	return src, parts, nil
}

func (s *APIServer) ledgerReviewDetail(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	tx, err := reviewTx(ctx, s.store.db, false)
	if err != nil {
		reviewFail(s, w, err)
		return
	}
	defer tx.Rollback()
	src, err := loadReviewSource(ctx, tx, r.URL.Query().Get("entry_key"))
	if err != nil {
		reviewFail(s, w, err)
		return
	}
	current, version, err := reviewCurrent(ctx, tx, src.Entry)
	if err != nil {
		reviewFail(s, w, err)
		return
	}
	orders := src.Candidates
	if po := strings.TrimSpace(r.URL.Query().Get("order_no")); po != "" {
		if len([]rune(po)) > 200 {
			reviewFail(s, w, reviewProblem(400, "订单号过长"))
			return
		}
		orders = []string{po}
	}
	if len(orders) > 100 {
		orders = orders[:100]
	}
	var products, history []byte
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('po_no',po_no,'products',products,'incomplete',incomplete) ORDER BY po_no),'[]') FROM profit_ledger_products WHERE shop_key=$1 AND po_no=ANY($2)`, src.Shop, pq.Array(orders)).Scan(&products)
	if err != nil {
		reviewFail(s, w, err)
		return
	}
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(h) ORDER BY h.id DESC),'[]') FROM(SELECT h.* FROM profit_ledger_review_history h JOIN profit_ledger_reviews r ON r.id=h.review_id WHERE r.entry_key=$1 ORDER BY h.id DESC LIMIT 100)h`, src.Entry).Scan(&history)
	if err != nil {
		reviewFail(s, w, err)
		return
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: map[string]any{"source": src, "review": current, "version": version, "orders": json.RawMessage(products), "history": json.RawMessage(history)}})
}
func (s *APIServer) ledgerReviewPreview(w http.ResponseWriter, r *http.Request) {
	s.writeLedgerReview(w, r, false)
}
func (s *APIServer) saveLedgerReview(w http.ResponseWriter, r *http.Request) {
	s.writeLedgerReview(w, r, true)
}
func (s *APIServer) writeLedgerReview(w http.ResponseWriter, r *http.Request, save bool) {
	if !financeWriteOK(w, r) {
		return
	}
	var input ledgerReviewInput
	if decodeFinanceBody(w, r, &input) != nil {
		reviewFail(s, w, reviewProblem(400, "核对数据格式无效"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	tx, err := reviewTx(ctx, s.store.db, save)
	if err != nil {
		reviewFail(s, w, err)
		return
	}
	defer tx.Rollback()
	if save {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('profit-review:'||$1))`, input.Entry); err != nil {
			reviewFail(s, w, err)
			return
		}
	}
	before, version, err := reviewCurrent(ctx, tx, input.Entry)
	if err != nil {
		reviewFail(s, w, err)
		return
	}
	if input.Version != version {
		reviewFail(s, w, reviewProblem(409, "核对记录已更新，请刷新后重试"))
		return
	}
	src, parts, err := prepareLedgerReview(ctx, tx, input)
	if err != nil {
		reviewFail(s, w, err)
		return
	}
	if !save {
		writeJSON(w, 200, apiResponse{Success: true, Data: map[string]any{"source": src, "allocations": parts, "kind": input.Kind, "version": version}})
		return
	}
	raw, _ := json.Marshal(parts)
	snapshot, _ := json.Marshal(src)
	var after []byte
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO profit_ledger_reviews(entry_key,shop_key,source_fingerprint,kind,method,allocations,reference,source_snapshot) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,$8::jsonb)
 ON CONFLICT(entry_key) DO UPDATE SET shop_key=EXCLUDED.shop_key,source_fingerprint=EXCLUDED.source_fingerprint,source_snapshot=EXCLUDED.source_snapshot,kind=EXCLUDED.kind,method=EXCLUDED.method,allocations=EXCLUDED.allocations,reference=EXCLUDED.reference,version=profit_ledger_reviews.version+1,updated_at=now(),deleted_at=NULL RETURNING id,to_jsonb(profit_ledger_reviews)`, src.Entry, src.Shop, src.Fingerprint, input.Kind, map[bool]string{true: "manual", false: input.Method}[input.Kind == "non_order"], string(raw), strings.TrimSpace(input.Reference), string(snapshot)).Scan(&id, &after)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO profit_ledger_review_history(review_id,action,before_payload,after_payload) VALUES($1,'save',$2::jsonb,$3::jsonb)`, id, string(before), string(after))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		var pg *pq.Error
		if errors.As(err, &pg) && (pg.Code == "40001" || pg.Code == "23505") {
			err = reviewProblem(409, "核对记录已更新，请重新读取")
		}
		reviewFail(s, w, err)
		return
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: json.RawMessage(after)})
}
func (s *APIServer) withdrawLedgerReview(w http.ResponseWriter, r *http.Request) {
	if !financeWriteOK(w, r) {
		return
	}
	var input struct {
		Entry     string `json:"entry_key"`
		Version   int    `json:"version"`
		Reference string `json:"reference"`
	}
	if decodeFinanceBody(w, r, &input) != nil || input.Entry == "" || input.Version < 1 || strings.TrimSpace(input.Reference) == "" || len([]rune(input.Reference)) > 1000 {
		reviewFail(s, w, reviewProblem(400, "须填写流水键、版本和撤销凭证"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tx, err := reviewTx(ctx, s.store.db, true)
	if err != nil {
		reviewFail(s, w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('profit-review:'||$1))`, input.Entry); err != nil {
		reviewFail(s, w, err)
		return
	}
	before, version, err := reviewCurrent(ctx, tx, input.Entry)
	if err != nil {
		reviewFail(s, w, err)
		return
	}
	if version != input.Version {
		reviewFail(s, w, reviewProblem(409, "核对记录已更新，请刷新"))
		return
	}
	var id int64
	var after []byte
	err = tx.QueryRowContext(ctx, `UPDATE profit_ledger_reviews SET deleted_at=now(),updated_at=now(),version=version+1 WHERE entry_key=$1 AND version=$2 AND deleted_at IS NULL RETURNING id,to_jsonb(profit_ledger_reviews)||jsonb_build_object('withdraw_reference',$3::text)`, input.Entry, input.Version, strings.TrimSpace(input.Reference)).Scan(&id, &after)
	if err == sql.ErrNoRows {
		reviewFail(s, w, reviewProblem(409, "核对记录已撤销或已更新"))
		return
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO profit_ledger_review_history(review_id,action,before_payload,after_payload) VALUES($1,'withdraw',$2::jsonb,$3::jsonb)`, id, string(before), string(after))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		var pg *pq.Error
		if errors.As(err, &pg) && pg.Code == "40001" {
			err = reviewProblem(409, "核对记录已更新，请重新读取")
		}
		reviewFail(s, w, err)
		return
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: json.RawMessage(after)})
}
