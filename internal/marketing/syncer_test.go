package marketing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pangu-sales-manager/internal/temu"
)

func TestSyncerRequestsAndRetainsOnlyCurrentActivities(t *testing.T) {
	var requestedTypes []string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Type          string `json:"type"`
			SessionStatus int    `json:"sessionStatus"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requestedTypes = append(requestedTypes, payload.Type)
		writer.Header().Set("Content-Type", "application/json")
		switch payload.Type {
		case temu.MarketingEnrollmentListAPI:
			if payload.SessionStatus != temu.CurrentSessionStatus {
				t.Errorf("sessionStatus = %d, want %d", payload.SessionStatus, temu.CurrentSessionStatus)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"success": true, "result": map[string]any{"total": 2, "list": []map[string]any{
				{"enrollId": 1, "assignSessionList": []map[string]any{{"sessionId": 11, "sessionStatus": 2, "siteId": 100}, {"sessionId": 12, "sessionStatus": 3, "siteId": 200}}, "skcList": []map[string]any{{"skcId": 10, "skuList": []map[string]any{{"skuId": 101, "sitePriceList": []map[string]any{{"siteId": 100, "activityPrice": 500}, {"siteId": 200, "activityPrice": 400}}}}}}},
				{"enrollId": 2, "assignSessionList": []map[string]any{{"sessionId": 21, "sessionStatus": 3, "siteId": 100}}},
			}}})
		default:
			t.Errorf("unexpected upstream API %q", payload.Type)
		}
	}))
	defer upstream.Close()

	syncer := NewSyncer(temu.NewClient(upstream.URL, "app", "secret", "token", time.Second))
	snapshot, err := syncer.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(requestedTypes) != 1 || requestedTypes[0] != temu.MarketingEnrollmentListAPI {
		t.Fatalf("upstream requests = %#v, want only current enrollments", requestedTypes)
	}
	if len(snapshot.Enrollments) != 1 || snapshot.Enrollments[0].EnrollID != 1 {
		t.Fatalf("current enrollments = %#v", snapshot.Enrollments)
	}
	enrollment := snapshot.Enrollments[0]
	if len(enrollment.AssignedSessions) != 1 || enrollment.AssignedSessions[0].SessionStatus != temu.CurrentSessionStatus {
		t.Fatalf("non-current sessions were retained: %#v", enrollment.AssignedSessions)
	}
	prices := enrollment.SKCList[0].SKUList[0].SitePriceList
	if len(prices) != 1 || prices[0].SiteID != 100 {
		t.Fatalf("non-current site prices were retained: %#v", prices)
	}
}
