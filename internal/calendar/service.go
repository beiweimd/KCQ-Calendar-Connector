// 本文件实现日期校验与 KCQ 日线时间戳适配，不访问外部数据源。
package calendar

import (
	"context"
	"strings"
	"time"
)

// Service 使用明确的数据仓库契约处理日历查询。
type Service struct {
	reader  Reader
	catalog *Catalog
}

// NewService 注入只读仓库和不可变市场配置。
func NewService(reader Reader, catalog *Catalog) *Service {
	return &Service{reader: reader, catalog: catalog}
}

// Markets 返回已经导入的市场及实际覆盖范围，未导入市场不宣称可用。
func (s *Service) Markets(ctx context.Context) ([]Coverage, error) {
	snapshots, err := s.reader.Snapshots(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]Coverage, 0, len(snapshots))
	for _, snapshot := range snapshots {
		market, err := s.catalog.Resolve(snapshot.Exchange)
		if err != nil {
			return nil, err
		}
		if snapshot.Source != market.Source {
			name, ok := market.AlternateCalendars[snapshot.Source]
			if !ok {
				return nil, Unsupported("unknown imported source %q for %s", snapshot.Source, market.Exchange)
			}
			market.Calendar = name
		}
		market.Source = snapshot.Source
		market.SourceURL = snapshot.SourceURL
		items = append(items, Coverage{Market: market, Snapshot: snapshot})
	}
	return items, nil
}

// ParseDate 只接受真实存在且格式完整的 ISO 日期。
func ParseDate(value string) (time.Time, error) {
	date, err := time.Parse(DateLayout, value)
	if err != nil || date.Format(DateLayout) != value {
		return time.Time{}, Invalid("date must be a valid YYYY-MM-DD value")
	}
	return date, nil
}

// Days 校验日期区间后查询全部日期或仅开市日期。
func (s *Service) Days(ctx context.Context, exchange, start, end string, openOnly bool) (DaysResult, error) {
	market, err := s.catalog.Resolve(exchange)
	if err != nil {
		return DaysResult{}, err
	}
	first, err := ParseDate(start)
	if err != nil {
		return DaysResult{}, err
	}
	last, err := ParseDate(end)
	if err != nil {
		return DaysResult{}, err
	}
	if last.Before(first) || last.Sub(first) > time.Duration(MaxRangeDays-1)*24*time.Hour {
		return DaysResult{}, Invalid("date range must be ordered and contain at most 3660 days")
	}
	return s.reader.Range(ctx, market.Exchange, start, end, openOnly)
}

// Next 返回指定日期之后的交易日；查询范围末尾通过 Complete 显式表达不足。
func (s *Service) Next(ctx context.Context, exchange, after string, count int) (NextResult, error) {
	market, err := s.catalog.Resolve(exchange)
	if err != nil {
		return NextResult{}, err
	}
	if _, err := ParseDate(after); err != nil {
		return NextResult{}, err
	}
	if count < 1 || count > MaxCount {
		return NextResult{}, Invalid("count must be between 1 and 4096")
	}
	return s.reader.Next(ctx, market.Exchange, after, count)
}

// Future 将真实交易日期映射为日线槽位；默认保持市场当地的锚点时分秒。
func (s *Service) Future(ctx context.Context, req TradingCalendarRequest) (TradingCalendar, error) {
	if strings.TrimSpace(req.SourceID) == "" || strings.TrimSpace(req.Instrument.Symbol) == "" || req.AnchorTimestamp <= 0 {
		return TradingCalendar{}, Invalid("sourceId, instrument.symbol and positive anchorTimestamp are required")
	}
	if req.Period != "daily" || req.BarAggregation != "original" {
		return TradingCalendar{}, Unsupported("only daily/original trading calendar is supported")
	}
	exchange := req.Instrument.Exchange
	if value, exists := req.Instrument.ProviderRef["calendarExchange"]; exists {
		text, ok := value.(string)
		if !ok || text == "" {
			return TradingCalendar{}, Invalid("calendarExchange must be a non-empty string")
		}
		exchange = text
	}
	market, err := s.catalog.Resolve(exchange)
	if err != nil {
		return TradingCalendar{}, err
	}
	zoneName := market.TimeZone
	if value, exists := req.Instrument.ProviderRef["calendarTimeZone"]; exists {
		text, ok := value.(string)
		if !ok || text == "" {
			return TradingCalendar{}, Invalid("calendarTimeZone must be a non-empty IANA timezone")
		}
		zoneName = text
	}
	zone, err := time.LoadLocation(zoneName)
	if err != nil {
		return TradingCalendar{}, Invalid("unknown calendarTimeZone")
	}
	anchor := time.UnixMilli(req.AnchorTimestamp).In(zone)
	if anchor.Year() < 1900 || anchor.Year() > 2200 {
		return TradingCalendar{}, Invalid("anchorTimestamp is outside supported date bounds")
	}
	result, err := s.Next(ctx, market.Exchange, anchor.Format(DateLayout), req.Count)
	if err != nil {
		return TradingCalendar{}, err
	}
	future := make([]int64, 0, len(result.Dates))
	for _, value := range result.Dates {
		date, err := ParseDate(value)
		if err != nil {
			return TradingCalendar{}, err
		}
		// 按当地日期重新构造时刻，夏令时跨界不能按固定 24 小时累加。
		stamp := time.Date(date.Year(), date.Month(), date.Day(), anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), zone).UnixMilli()
		future = append(future, stamp)
	}
	return TradingCalendar{AnchorTimestamp: req.AnchorTimestamp, FutureTimestamps: future}, nil
}

// Ping 检查底层数据库是否仍可读。
func (s *Service) Ping(ctx context.Context) error { return s.reader.Ping(ctx) }
