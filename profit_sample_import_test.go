package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestImportRealTemuProfitZip(t *testing.T) {
	if os.Getenv("PROFIT_IMPORT_SAMPLE") == "" {
		t.Skip("set PROFIT_IMPORT_SAMPLE=1 to import the sample zip into local postgres")
	}
	data, err := os.ReadFile("TEMU利润计算.zip")
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	store := openTestProfitStore(t, ctx)
	if store == nil {
		return
	}
	defer store.Close()

	_, batches, err := parseProfitUpload("TEMU利润计算.zip", data, profitParseOptions{
		ShopKey: "panda-homes", Location: time.FixedZone("CST", 8*60*60),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.applyProfitImport(ctx, "panda-homes", batches)
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesImported < 8 || result.RowsUpserted < 30000 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	t.Logf("imported files=%d rows=%d", result.FilesImported, result.RowsUpserted)
}
