// 本文件以真实 SQLite 为底层验证 HTTP 契约和 KCQ V1 envelope。
package httpapi_test

import (
	"encoding/json"
	"io"
	"kcq-calendar-connector/internal/calendar"
	"kcq-calendar-connector/internal/httpapi"
	"kcq-calendar-connector/internal/storage"
	"kcq-calendar-connector/internal/testsupport"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// handler 为每个 HTTP 测试装配完整的生产处理链路。
func handler(t *testing.T) http.Handler {
	t.Helper()
	store, err := storage.Open(testsupport.Database(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	catalog, err := calendar.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return httpapi.New(calendar.NewService(store, catalog), slog.New(slog.NewTextHandler(io.Discard, nil)), []string{"http://localhost:5173"})
}

// TestRoutes 验证成功查询、错误状态和所有响应的请求标识。
func TestRoutes(t *testing.T) {
	h := handler(t)
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodGet, httpapi.HealthPath, "", 200},
		{http.MethodGet, httpapi.MarketsPath, "", 200},
		{http.MethodGet, "/api/v1/calendars/NYSE/days?start=2026-11-25&end=2026-11-27&openOnly=true", "", 200},
		{http.MethodGet, "/api/v1/calendars/SSE/next?after=2026-11-27&count=2", "", 200},
		{http.MethodGet, "/api/v1/calendars/SSE/days?start=2027-01-01&end=2027-01-02", "", 422},
		{http.MethodGet, "/api/v1/calendars/FUTURES/next?after=2026-11-01&count=2", "", 422},
		{http.MethodGet, "/api/v1/calendars/SSE/days?start=2026-11-01&end=2026-11-02&openOnly=maybe", "", 400},
		{http.MethodPost, httpapi.V1Path, `{"sourceId":"gotdx","instrument":{"symbol":"600519","exchange":"SH"},"period":"daily","adjustment":"none","barAggregation":"original","anchorTimestamp":1795737600000,"count":3}`, 200},
		{http.MethodPost, httpapi.V1Path, `{"sourceId":"gotdx","instrument":{"symbol":"600519","exchange":"SH"},"period":"5min","barAggregation":"original","anchorTimestamp":1795737600000,"count":3}`, 422},
		{http.MethodPost, httpapi.V1Path, "{} {}", 400},
		{http.MethodPost, httpapi.MarketsPath, "", 405},
		{http.MethodGet, "/unknown", "", 404},
	} {
		t.Run(test.method+test.path+test.body, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)))
			if w.Code != test.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			var result struct {
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.RequestID == "" || result.RequestID != w.Header().Get(httpapi.RequestIDHeader) {
				t.Fatal("missing or inconsistent request ID")
			}
		})
	}
}

// TestCORS 验证允许的预检和不允许的网页来源。
func TestCORS(t *testing.T) {
	h := handler(t)
	for _, test := range []struct {
		origin string
		status int
	}{{"http://localhost:5173", 204}, {"https://untrusted.example", 403}} {
		r := httptest.NewRequest(http.MethodOptions, httpapi.V1Path, nil)
		r.Header.Set("Origin", test.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("unexpected CORS response: %d", w.Code)
		}
	}
}
