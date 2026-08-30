package main

import (
	"encoding/json"
	"time"
)

type SourceOrder struct {
	Platform         string
	ShopKey          string
	OrderNo          string
	SourceStatus     string
	NormalizedStatus string
	OccurredAt       time.Time
	OccurredAtSource string
	WarehouseCode    string
	SalesEligible    bool
	FirstSeenAt      time.Time
	UpdatedAt        time.Time
	RawPayload       json.RawMessage
}

type SourceLine struct {
	Platform         string
	ShopKey          string
	OrderNo          string
	SourceLineKey    string
	PlatformSKU      string
	ProductSKUID     int64
	SuggestedSKU     string
	ProductName      string
	VariantName      string
	WarehouseCode    string
	Quantity         float64
	ConversionFactor float64
	MappingSource    string
	MappingStatus    string
	UnitPrice        *float64
	Currency         string
	RawPayload       json.RawMessage
}

type InventoryRow struct {
	WarehouseCode string
	WarehouseName string
	WarehouseSKU  string
	Total         float64
	Available     float64
	Locked        float64
	InTransit     float64
	StatisticDate *time.Time
	UpdatedAt     *time.Time
}

type Warehouse struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}
