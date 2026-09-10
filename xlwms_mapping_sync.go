package main

import (
	"context"
	"fmt"

	"pangu-sales-manager/internal/xlwms"
)

// temuStoreCodes maps our shop_key to the store_code xlwms-api-manager uses.
// Hardcoded like the shop options already baked into web/index.html — these
// two Temu shops are the only ones with a real fulfillment mapping to sync.
var temuStoreCodes = map[string]string{
	"panda-homes": "634418225219052",
	"panda-buy":   "634418227898865",
}

// syncXLWMSMappings pulls the authoritative platform-SKU <-> warehouse-SKU
// mapping from xlwms-api-manager for every known Temu shop and overwrites the
// local sku_mappings cache. The xlwms "account" concept never leaves the
// xlwms.Client — this only ever thinks in terms of shop_key/store_code.
func (s *Syncer) syncXLWMSMappings(ctx context.Context) error {
	if s.xlwmsClient == nil {
		return nil
	}
	var errs []error
	for shopKey, storeCode := range temuStoreCodes {
		mappings, skippedBundles, err := s.xlwmsClient.ListMappings(ctx, storeCode)
		if err != nil {
			errs = append(errs, fmt.Errorf("list xlwms mappings for %s: %w", shopKey, err))
			continue
		}
		upserted, err := s.store.applyXLWMSMappings(ctx, shopKey, mappings)
		if err != nil {
			errs = append(errs, fmt.Errorf("apply xlwms mappings for %s: %w", shopKey, err))
			continue
		}
		s.logger.Info("xlwms SKU mapping sync completed", "shop", shopKey,
			"mappings", upserted, "skipped_bundles", skippedBundles)
	}
	if len(errs) == 0 {
		return nil
	}
	joined := errs[0]
	for _, err := range errs[1:] {
		joined = fmt.Errorf("%w; %w", joined, err)
	}
	return joined
}

// applyXLWMSMappings overwrites sku_mappings for platform='temu'/shopKey with
// the given authoritative mapping set, inside a single transaction.
func (s *Store) applyXLWMSMappings(ctx context.Context, shopKey string, mappings map[string]xlwms.Mapping) (int, error) {
	if len(mappings) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for platformSKU, mapping := range mappings {
		if err := s.upsertXLWMSMapping(ctx, tx, "temu", shopKey, platformSKU, mapping.WarehouseSKU, float64(mapping.Quantity)); err != nil {
			return 0, fmt.Errorf("upsert mapping %s: %w", platformSKU, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(mappings), nil
}
