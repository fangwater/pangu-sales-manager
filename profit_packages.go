package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type profitPackageLink struct {
	Source    string    `json:"source"`
	SourceKey string    `json:"source_key"`
	PO        string    `json:"po_no"`
	Child     string    `json:"child_order_no"`
	Package   string    `json:"package_no"`
	Tracking  string    `json:"tracking_no"`
	Updated   time.Time `json:"source_updated_at"`
}

// Package evidence has its own refresh schedule: order-list updates do not
// imply shipment updates. Each successful snapshot replaces one shop atomically.
func (s *Syncer) syncProfitPackages(ctx context.Context, shop, schema string) error {
	if schema != "temu_panda_homes" && schema != "temu_panda_buy" {
		return fmt.Errorf("unsupported Temu schema")
	}
	var due bool
	if err := s.store.db.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM profit_package_sync WHERE shop_key=$1 AND succeeded_at>now()-interval '15 minutes')`, shop).Scan(&due); err != nil {
		return err
	}
	if !due && !s.forceFull {
		return nil
	}
	links, err := s.readProfitPackages(ctx, schema)
	if err == nil {
		err = s.store.replaceProfitPackages(ctx, shop, links)
	}
	if err != nil {
		// Keep the prior successful snapshot and expose its age and last failure.
		_, saveErr := s.store.db.ExecContext(ctx, `INSERT INTO profit_package_sync(shop_key,attempted_at,error_message) VALUES($1,now(),$2) ON CONFLICT(shop_key) DO UPDATE SET attempted_at=now(),error_message=EXCLUDED.error_message`, shop, err.Error())
		if saveErr != nil {
			return fmt.Errorf("%w; record failure: %v", err, saveErr)
		}
	}
	return err
}

func (s *Syncer) readProfitPackages(ctx context.Context, schema string) ([]profitPackageLink, error) {
	if schema != "temu_panda_homes" && schema != "temu_panda_buy" {
		return nil, fmt.Errorf("unsupported Temu schema")
	}
	tx, err := s.temu.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := fmt.Sprintf(`
 SELECT 'order_detail',d.parent_order_sn,d.parent_order_sn,COALESCE(l->>'orderSn',''),p->>'packageSn','',d.fetched_at
 FROM %[1]s.temu_order_details d
 CROSS JOIN LATERAL jsonb_array_elements(COALESCE(d.detail_payload->'result'->'orderList','[]'::jsonb)) l
 CROSS JOIN LATERAL jsonb_array_elements(COALESCE(l->'packageSnInfo','[]'::jsonb)) p
 WHERE d.detail_payload->>'success'='true' AND p->>'callSuccess'='true' AND COALESCE(p->>'packageSn','')<>''
 UNION ALL
 SELECT 'shipment',s.id::text,o.parent_order_sn,'',p,COALESCE(s.tracking_number,''),s.updated_at
 FROM %[1]s.temu_shipments s JOIN %[1]s.temu_shipment_orders o ON o.shipment_id=s.id
 CROSS JOIN LATERAL unnest(s.package_sn_list) p
 WHERE s.status='shipped' AND p<>''`, schema)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	links := []profitPackageLink{}
	for rows.Next() {
		var l profitPackageLink
		if err := rows.Scan(&l.Source, &l.SourceKey, &l.PO, &l.Child, &l.Package, &l.Tracking, &l.Updated); err != nil {
			rows.Close()
			return nil, err
		}
		links = append(links, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return links, nil
}

func (s *Store) replaceProfitPackages(ctx context.Context, shop string, links []profitPackageLink) error {
	for _, link := range links {
		if link.PO == "" || link.Package == "" || link.SourceKey == "" || link.Updated.IsZero() || (link.Source != "shipment" && link.Source != "order_detail") {
			return fmt.Errorf("invalid package evidence")
		}
	}
	payload, err := json.Marshal(links)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('profit-packages:'||$1,0))`, shop); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM profit_order_packages WHERE shop_key=$1`, shop); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO profit_order_packages(shop_key,source,source_key,po_no,child_order_no,package_no,tracking_no,source_updated_at)
 SELECT DISTINCT $1,source,source_key,po_no,child_order_no,package_no,tracking_no,source_updated_at
 FROM jsonb_to_recordset($2::jsonb) AS x(source text,source_key text,po_no text,child_order_no text,package_no text,tracking_no text,source_updated_at timestamptz)`, shop, string(payload))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO profit_package_sync(shop_key,attempted_at,succeeded_at,link_count,error_message) VALUES($1,now(),now(),$2,'')
 ON CONFLICT(shop_key) DO UPDATE SET attempted_at=now(),succeeded_at=now(),link_count=EXCLUDED.link_count,error_message=''`, shop, len(links))
	if err != nil {
		return err
	}
	return tx.Commit()
}
