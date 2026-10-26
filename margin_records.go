package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Additional business evidence never overwrites an imported platform bill.
type marginRecord struct {
	ID        int64  `json:"id,omitempty"`
	Version   int    `json:"version"`
	Kind      string `json:"kind"`
	SourceKey string `json:"source_key,omitempty"`
	Shop      string `json:"shop_key,omitempty"`
	Category  string `json:"category,omitempty"`
	Order     string `json:"order_no,omitempty"`
	SKU       string `json:"platform_sku,omitempty"`
	Currency  string `json:"currency,omitempty"`
	Amount    string `json:"amount,omitempty"`
	Date      string `json:"date,omitempty"`
	PaidDate  string `json:"paid_date,omitempty"`
	PaidAt    string `json:"paid_at,omitempty"`
	Reference string `json:"reference,omitempty"`
	Entry     string `json:"entry_key,omitempty"`
	Site      string `json:"site,omitempty"`
	ProductID string `json:"product_id,omitempty"`
	Scope     string `json:"scope,omitempty"`
	Start     string `json:"start,omitempty"`
	End       string `json:"end,omitempty"`
	Confirmed *bool  `json:"confirmed,omitempty"`
}

var marginNumber = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})(\.[0-9]{1,10})?$`)

func validateMarginDay(value string, required bool) error {
	if value == "" && !required {
		return nil
	}
	d, e := time.Parse("2006-01-02", value)
	if e != nil || d.Format("2006-01-02") != value {
		return errors.New("日期须为 YYYY-MM-DD")
	}
	return nil
}
func (r marginRecord) key() (string, error) {
	for _, v := range []string{r.SourceKey, r.Shop, r.Category, r.Order, r.SKU, r.Entry, r.Site, r.ProductID, r.Reference} {
		if len([]rune(v)) > 1000 {
			return "", errors.New("字段超过 1000 字")
		}
	}
	if r.Reference == "" {
		return "", errors.New("须填写来源凭证")
	}
	if r.PaidAt != "" {
		if _, err := time.Parse(time.RFC3339, r.PaidAt); err != nil {
			return "", errors.New("付款时间须包含日期、时间和时区")
		}
	}
	var key []string
	switch r.Kind {
	case "adjustment":
		if r.Shop == "" || r.SourceKey == "" || !map[string]bool{"tax_withheld": true, "tax_refund": true, "non_order_fee": true, "other_income": true, "other_expense": true}[r.Category] {
			return "", errors.New("店铺、业务键或款项类别无效")
		}
		if !map[string]bool{"CNY": true, "USD": true, "EUR": true, "HKD": true}[r.Currency] || !marginNumber.MatchString(r.Amount) {
			return "", errors.New("币种或金额无效；金额填写非负数")
		}
		if err := validateMarginDay(r.Date, true); err != nil {
			return "", err
		}
		if err := validateMarginDay(r.PaidDate, false); err != nil {
			return "", err
		}
		key = []string{r.Shop, r.SourceKey}
	case "metadata":
		if err := validateMarginDay(r.Date, false); err != nil {
			return "", err
		}
		if r.Entry == "" || (r.Date == "" && r.PaidDate == "" && r.PaidAt == "" && r.Site == "" && r.ProductID == "") {
			return "", errors.New("须填写原始流水键及补充信息")
		}
		if err := validateMarginDay(r.PaidDate, false); err != nil {
			return "", err
		}
		key = []string{r.Entry}
	case "coverage":
		if r.Shop == "" || r.Confirmed == nil || !map[string]bool{"statements": true, "taxes": true}[r.Scope] {
			return "", errors.New("完整性范围、店铺或确认状态无效")
		}
		if err := validateMarginDay(r.Start, true); err != nil {
			return "", err
		}
		if err := validateMarginDay(r.End, true); err != nil {
			return "", err
		}
		if r.Start > r.End {
			return "", errors.New("开始日期晚于截止日期")
		}
		key = []string{r.Shop, r.Scope, r.Start, r.End}
	default:
		return "", errors.New("记录类型无效")
	}
	value, _ := json.Marshal(key)
	return string(value), nil
}
func financeWriteOK(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Pangu-Request") != "finance" {
		writeJSON(w, 403, apiResponse{Success: false, Error: "请求来源无效"})
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || u.Host != r.Host {
			writeJSON(w, 403, apiResponse{Success: false, Error: "请求来源无效"})
			return false
		}
	}
	return true
}
func decodeFinanceBody(w http.ResponseWriter, r *http.Request, value any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	d.DisallowUnknownFields()
	if e := d.Decode(value); e != nil {
		return e
	}
	var tail any
	if d.Decode(&tail) != io.EOF {
		return errors.New("额外 JSON 数据")
	}
	return nil
}
func saveMarginRecord(ctx context.Context, tx *sql.Tx, r marginRecord, idempotent bool) (int64, bool, error) {
	key, err := r.key()
	if err != nil {
		return 0, false, err
	}
	if r.Version < 0 {
		return 0, false, errors.New("版本无效")
	}
	var before []byte
	var version int
	var deleted sql.NullTime
	if r.ID > 0 {
		err = tx.QueryRowContext(ctx, `SELECT payload,version,deleted_at FROM profit_accounting_records WHERE id=$1 AND kind=$2 FOR UPDATE`, r.ID, r.Kind).Scan(&before, &version, &deleted)
		if err != nil {
			return 0, false, errors.New("记录不存在")
		}
		if version != r.Version || deleted.Valid {
			return 0, false, errMarginConflict
		}
	} else if r.Version != 0 {
		return 0, false, errMarginConflict
	}
	clean := r
	clean.ID = 0
	clean.Version = 0
	payload, _ := json.Marshal(clean)
	var existingID int64
	var same, active bool
	err = tx.QueryRowContext(ctx, `SELECT id,payload=$3::jsonb,deleted_at IS NULL FROM profit_accounting_records WHERE kind=$1 AND record_key=$2 FOR UPDATE`, r.Kind, key, string(payload)).Scan(&existingID, &same, &active)
	if err != nil && err != sql.ErrNoRows {
		return 0, false, err
	}
	if existingID > 0 && existingID != r.ID {
		if idempotent && same && active {
			return existingID, true, nil
		}
		if !active && r.ID == 0 {
			r.ID = existingID
			if err = tx.QueryRowContext(ctx, `SELECT payload,version FROM profit_accounting_records WHERE id=$1`, r.ID).Scan(&before, &version); err != nil {
				return 0, false, err
			}
		} else {
			return 0, false, errMarginConflict
		}
	}
	if r.ID > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE profit_accounting_records SET record_key=$2,payload=$3::jsonb,version=version+1,deleted_at=NULL,updated_at=now() WHERE id=$1`, r.ID, key, string(payload))
	} else {
		err = tx.QueryRowContext(ctx, `INSERT INTO profit_accounting_records(kind,record_key,payload) VALUES($1,$2,$3::jsonb) RETURNING id`, r.Kind, key, string(payload)).Scan(&r.ID)
	}
	if err != nil {
		return 0, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO profit_accounting_history(record_id,action,before_payload,after_payload) VALUES($1,$2,$3::jsonb,$4::jsonb)`, r.ID, map[bool]string{true: "update", false: "create"}[version > 0], nullableJSON(before), string(payload))
	return r.ID, false, err
}

var errMarginConflict = errors.New("记录已更新或业务键重复，请刷新后编辑原记录")

func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
func (s *APIServer) marginRecords(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT payload||jsonb_build_object('id',id,'version',version,'created_at',created_at,'updated_at',updated_at) FROM profit_accounting_records WHERE deleted_at IS NULL ORDER BY id DESC`)
	if err != nil {
		s.internalError(w, "read finance evidence", err)
		return
	}
	defer rows.Close()
	data := []json.RawMessage{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			s.internalError(w, "scan finance evidence", err)
			return
		}
		data = append(data, json.RawMessage(b))
	}
	if err = rows.Err(); err != nil {
		s.internalError(w, "read finance evidence rows", err)
		return
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: map[string]any{"records": data}})
}
func (s *APIServer) saveMarginRecords(w http.ResponseWriter, r *http.Request) {
	if !financeWriteOK(w, r) {
		return
	}
	var input struct {
		Rows []marginRecord `json:"rows"`
		CSV  string         `json:"csv"`
		Kind string         `json:"kind"`
	}
	if decodeFinanceBody(w, r, &input) != nil {
		writeJSON(w, 400, apiResponse{Success: false, Error: "财务补录格式无效"})
		return
	}
	if input.CSV != "" {
		var err error
		input.Rows, err = marginCSV(input.Kind, input.CSV)
		if err != nil {
			writeJSON(w, 400, apiResponse{Success: false, Error: err.Error()})
			return
		}
	}
	if len(input.Rows) < 1 || len(input.Rows) > 5000 {
		writeJSON(w, 400, apiResponse{Success: false, Error: "须提交 1–5000 条记录"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		s.internalError(w, "begin finance evidence", err)
		return
	}
	defer tx.Rollback()
	ids := []int64{}
	unchanged := 0
	for _, row := range input.Rows {
		id, same, e := saveMarginRecord(ctx, tx, row, input.CSV != "")
		if e != nil {
			status := 400
			if errors.Is(e, errMarginConflict) {
				status = 409
			}
			writeJSON(w, status, apiResponse{Success: false, Error: e.Error()})
			return
		}
		ids = append(ids, id)
		if same {
			unchanged++
		}
	}
	if err = tx.Commit(); err != nil {
		s.internalError(w, "commit finance evidence", err)
		return
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: map[string]any{"ids": ids, "created_or_updated": len(ids) - unchanged, "unchanged": unchanged}})
}
func (s *APIServer) removeMarginRecord(w http.ResponseWriter, r *http.Request) {
	if !financeWriteOK(w, r) {
		return
	}
	var input struct {
		ID      int64 `json:"id"`
		Version int   `json:"version"`
	}
	if decodeFinanceBody(w, r, &input) != nil || input.ID < 1 || input.Version < 1 {
		writeJSON(w, 400, apiResponse{Success: false, Error: "记录或版本无效"})
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.internalError(w, "begin evidence deletion", err)
		return
	}
	defer tx.Rollback()
	var before []byte
	err = tx.QueryRowContext(r.Context(), `UPDATE profit_accounting_records SET deleted_at=now(),updated_at=now(),version=version+1 WHERE id=$1 AND version=$2 AND deleted_at IS NULL RETURNING payload`, input.ID, input.Version).Scan(&before)
	if err == sql.ErrNoRows {
		writeJSON(w, 409, apiResponse{Success: false, Error: errMarginConflict.Error()})
		return
	}
	if err != nil {
		s.internalError(w, "delete finance evidence", err)
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO profit_accounting_history(record_id,action,before_payload) VALUES($1,'delete',$2::jsonb)`, input.ID, string(before))
	if err != nil {
		s.internalError(w, "audit evidence deletion", err)
		return
	}
	if err = tx.Commit(); err != nil {
		s.internalError(w, "commit evidence deletion", err)
		return
	}
	writeJSON(w, 200, apiResponse{Success: true, Data: map[string]any{"id": input.ID}})
}
func marginCSV(kind, content string) ([]marginRecord, error) {
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(content, "\ufeff")))
	all, e := reader.ReadAll()
	if e != nil || len(all) < 2 {
		return nil, errors.New("CSV 格式无效或无明细")
	}
	header := map[string]int{}
	for i, v := range all[0] {
		header[v] = i
	}
	out := []marginRecord{}
	for _, row := range all[1:] {
		v := func(name string) string {
			if i, ok := header[name]; ok && i < len(row) {
				return strings.TrimSpace(row[i])
			}
			return ""
		}
		r := marginRecord{Kind: kind, SourceKey: v("业务键"), Shop: v("店铺"), Category: v("款项类别"), Order: v("订单号"), SKU: v("销售SKU"), Currency: v("币种"), Amount: v("金额"), Date: v("费用日期"), PaidDate: v("付款日期"), PaidAt: v("付款时间（北京时间）"), Reference: v("来源凭证"), Entry: v("流水键"), Site: v("站点"), ProductID: v("产品ID"), Scope: v("范围"), Start: v("开始日期"), End: v("截止日期")}
		if r.PaidAt != "" {
			location, _ := time.LoadLocation("Asia/Shanghai")
			for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02T15:04"} {
				if v, err := time.ParseInLocation(layout, r.PaidAt, location); err == nil {
					r.PaidAt = v.Format(time.RFC3339)
					break
				}
			}
		}
		if translated, ok := map[string]string{"税金代扣": "tax_withheld", "税金退回": "tax_refund", "非订单交易费": "non_order_fee", "其他收入": "other_income", "其他支出": "other_expense"}[r.Category]; ok {
			r.Category = translated
		}
		if translated, ok := map[string]string{"财务账单": "statements", "税费及非订单费用": "taxes"}[r.Scope]; ok {
			r.Scope = translated
		}
		if kind == "coverage" {
			confirmed := v("已确认完整")
			if confirmed != "是" && confirmed != "否" {
				return nil, fmt.Errorf("已确认完整须为是或否")
			}
			b := confirmed == "是"
			r.Confirmed = &b
		}
		out = append(out, r)
	}
	return out, nil
}
