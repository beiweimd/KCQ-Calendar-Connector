// 本文件使用实际采集样本和 SQLite 验证市场边界及时间戳口径。
package calendar_test

import (
	"context"
	"kcq-calendar-connector/internal/calendar"
	"kcq-calendar-connector/internal/storage"
	"kcq-calendar-connector/internal/testsupport"
	"reflect"
	"testing"
	"time"
)

// service 为每个测试创建独立的真实数据库和领域服务。
func service(t *testing.T) *calendar.Service {
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
	return calendar.NewService(store, catalog)
}

// TestThanksgiving 验证感恩节美国休市、中国开市，避免跨市场套用工作日。
func TestThanksgiving(t *testing.T) {
	s := service(t)
	for _, test := range []struct {
		exchange string
		open     bool
	}{{"NYSE", false}, {"NASDAQ", false}, {"SH", true}, {"HK", true}} {
		t.Run(test.exchange, func(t *testing.T) {
			result, err := s.Days(context.Background(), test.exchange, "2026-11-26", "2026-11-26", false)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Days) != 1 || result.Days[0].IsOpen != test.open {
				t.Fatalf("unexpected days: %+v", result.Days)
			}
		})
	}
}

// TestFutureClock 验证市场当地时刻和显式 UTC 锚点在夏令时结束时分别保持各自口径。
func TestFutureClock(t *testing.T) {
	s := service(t)
	for _, test := range []struct {
		name         string
		anchor, want time.Time
		provider     map[string]any
	}{
		{"market timezone", time.Date(2026, 10, 30, 13, 30, 0, 0, time.UTC), time.Date(2026, 11, 2, 14, 30, 0, 0, time.UTC), nil},
		{"UTC midnight bars", time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), map[string]any{"calendarTimeZone": "UTC"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := s.Future(context.Background(), calendar.TradingCalendarRequest{SourceID: "test-connector", Instrument: calendar.InstrumentReference{Symbol: "AAPL", Exchange: "NASDAQ", ProviderRef: test.provider}, Period: "daily", Adjustment: "qfq", BarAggregation: "original", AnchorTimestamp: test.anchor.UnixMilli(), Count: 1})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.FutureTimestamps, []int64{test.want.UnixMilli()}) {
				t.Fatalf("got %v, want %d", result.FutureTimestamps, test.want.UnixMilli())
			}
		})
	}
}

// TestNextPrefix 验证假日跳过、排他锚点以及覆盖末尾的显式不完整结果。
func TestNextPrefix(t *testing.T) {
	s := service(t)
	result, err := s.Next(context.Background(), "XNYS", "2026-11-25", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Dates, []string{"2026-11-27", "2026-11-30"}) || result.Complete {
		t.Fatalf("unexpected prefix: %+v", result)
	}
	end, err := s.Next(context.Background(), "XNYS", "2026-11-30", 1)
	if err != nil || end.Dates == nil || len(end.Dates) != 0 || end.Complete {
		t.Fatalf("unexpected coverage end: %+v, %v", end, err)
	}
}

// TestInvalidQueries 验证未知市场、日期缺口、逆序和越界不能静默返回空成功。
func TestInvalidQueries(t *testing.T) {
	s := service(t)
	for _, test := range []struct{ exchange, start, end string }{
		{"FUTURES", "2026-11-01", "2026-11-02"}, {"SSE", "2026-02-30", "2026-11-02"},
		{"SSE", "2026-11-03", "2026-11-02"}, {"SSE", "2027-01-01", "2027-01-02"},
	} {
		if _, err := s.Days(context.Background(), test.exchange, test.start, test.end, false); err == nil {
			t.Fatalf("accepted invalid query: %+v", test)
		}
	}
	if _, err := s.Next(context.Background(), "SSE", "2026-11-01", 0); err == nil {
		t.Fatal("accepted zero count")
	}
	if _, err := calendar.ParseDate("2026-1-01"); err == nil {
		t.Fatal("accepted non-ISO date")
	}
}
