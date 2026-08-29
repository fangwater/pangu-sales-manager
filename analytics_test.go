package main

import (
	"math"
	"testing"
	"time"
)

func TestForecastDemandUsesRecentDemand(t *testing.T) {
	series := make([]float64, 90)
	for index := range series {
		series[index] = 2
	}
	forecast := forecastDemand(series)
	if forecast.Confidence != "high" {
		t.Fatalf("confidence = %q", forecast.Confidence)
	}
	if math.Abs(forecast.Next7Days-14) > 0.1 || math.Abs(forecast.Next30Days-60) > 0.1 {
		t.Fatalf("unexpected forecast: %+v", forecast)
	}
}

func TestForecastDemandMarksSparseHistoryLowConfidence(t *testing.T) {
	series := make([]float64, 90)
	series[88] = 4
	series[89] = 3
	forecast := forecastDemand(series)
	if forecast.Confidence != "low" || forecast.HistoryDays != 2 {
		t.Fatalf("unexpected sparse forecast: %+v", forecast)
	}
}

func TestPeriodWindowsAreCalendarAligned(t *testing.T) {
	location := time.FixedZone("test", 8*60*60)
	today := time.Date(2026, 8, 21, 0, 0, 0, 0, location)
	week, _ := periodWindows("week", today)
	if week.Weekday() != time.Monday {
		t.Fatalf("week starts on %s", week.Weekday())
	}
	month, _ := periodWindows("month", today)
	if month.Day() != 1 {
		t.Fatalf("month starts on day %d", month.Day())
	}
}

func TestMatchWarehouseSKURemovesNumericWarehouseSuffix(t *testing.T) {
	inventory := []InventoryRow{{WarehouseSKU: "VH-20pcs-Pink-45cm"}, {WarehouseSKU: "VH-20pcs-Black-45cm"}}
	matched, ok := matchWarehouseSKU("VH-20pcs-Pink-45cm-21", inventory)
	if !ok || matched != "VH-20pcs-Pink-45cm" {
		t.Fatalf("match = %q, %v", matched, ok)
	}
	if _, ok := matchWarehouseSKU("VH-20pcs-Pink-45cm-custom", inventory); ok {
		t.Fatal("non-numeric suffix must not be inferred")
	}
}

func TestMatchWarehouseSKURepairsMissingCentimeterSuffix(t *testing.T) {
	inventory := []InventoryRow{{WarehouseSKU: "NSH+H-50Pcs-Orange-42cm"}}
	matched, ok := matchWarehouseSKU("NSH+H-50Pcs-Orange-42c", inventory)
	if !ok || matched != "NSH+H-50Pcs-Orange-42cm" {
		t.Fatalf("match = %q, %v", matched, ok)
	}
}
