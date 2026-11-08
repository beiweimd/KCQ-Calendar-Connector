// Package calendar 定义交易日历的公共契约，不依赖 HTTP 或 SQLite 实现。
package calendar

import (
	"context"
	"fmt"
)

// 稳定错误码沿用 KCQ market-data V1 的分类。
const (
	CodeInvalidRequest = "INVALID_REQUEST"
	CodeUnsupported    = "UNSUPPORTED_CAPABILITY"
	CodeInternal       = "INTERNAL"
	DateLayout         = "2006-01-02"
	MaxCount           = 4096
	MaxRangeDays       = 3660
	SchemaVersion      = 1
)

// Error 表示可直接向调用方说明的参数或能力错误。
type Error struct {
	Code    string
	Message string
}

// Error 返回错误的人类可读说明。
func (e *Error) Error() string { return e.Message }

// Unsupported 为明确不支持的市场、周期或日期范围构造错误。
func Unsupported(format string, args ...any) error {
	return &Error{Code: CodeUnsupported, Message: fmt.Sprintf(format, args...)}
}

// Invalid 为错误的输入构造错误。
func Invalid(message string) error { return &Error{Code: CodeInvalidRequest, Message: message} }

// Market 定义市场标识、时区以及离线日历数据来源。
type Market struct {
	Exchange           string            `json:"exchange"`
	Name               string            `json:"name"`
	TimeZone           string            `json:"timeZone"`
	Aliases            []string          `json:"aliases"`
	Source             string            `json:"source"`
	Calendar           string            `json:"calendar"`
	SourceURL          string            `json:"sourceUrl"`
	AlternateCalendars map[string]string `json:"alternateCalendars,omitempty"`
}

// Snapshot 记录一个市场最近一次原子更新的数据覆盖范围及来源版本。
type Snapshot struct {
	Exchange      string `json:"exchange"`
	StartDate     string `json:"startDate"`
	EndDate       string `json:"endDate"`
	Source        string `json:"source"`
	SourceVersion string `json:"sourceVersion"`
	SourceURL     string `json:"sourceUrl"`
	GeneratedAt   string `json:"generatedAt"`
	Revision      string `json:"revision"`
	RowCount      int    `json:"rowCount"`
	OverrideCount int    `json:"overrideCount"`
}

// Coverage 同时返回市场定义和实际已导入的数据范围。
type Coverage struct {
	Market   Market   `json:"market"`
	Snapshot Snapshot `json:"snapshot"`
}

// Day 表示某个自然日期是否为该市场的计划交易日。
type Day struct {
	Date              string `json:"date"`
	IsOpen            bool   `json:"isOpen"`
	OverrideReason    string `json:"overrideReason,omitempty"`
	OverrideSourceURL string `json:"overrideSourceUrl,omitempty"`
}

// DaysResult 返回一个有明确覆盖范围的日期区间。
type DaysResult struct {
	Snapshot Snapshot `json:"snapshot"`
	Days     []Day    `json:"days"`
}

// NextResult 在覆盖末尾返回实际前缀，并通过 Complete 明确是否凑齐数量。
type NextResult struct {
	Snapshot Snapshot `json:"snapshot"`
	Dates    []string `json:"dates"`
	Complete bool     `json:"complete"`
}

// Reader 要求同一次查询的快照元数据与日期记录具有一致的读取视图。
type Reader interface {
	Snapshots(context.Context) ([]Snapshot, error)
	Range(context.Context, string, string, string, bool) (DaysResult, error)
	Next(context.Context, string, string, int) (NextResult, error)
	Ping(context.Context) error
}

// InstrumentReference 接受行情 Connector 的标识；ProviderRef 可显式声明日历市场和锚点时区。
type InstrumentReference struct {
	Symbol      string         `json:"symbol"`
	Exchange    string         `json:"exchange"`
	ProviderRef map[string]any `json:"providerRef"`
}

// TradingCalendarRequest 保持 KCQ V1 请求形状，仅实现已确认的 daily/original 口径。
type TradingCalendarRequest struct {
	SourceID        string              `json:"sourceId"`
	Instrument      InstrumentReference `json:"instrument"`
	Period          string              `json:"period"`
	Adjustment      string              `json:"adjustment"`
	BarAggregation  string              `json:"barAggregation"`
	AnchorTimestamp int64               `json:"anchorTimestamp"`
	Count           int                 `json:"count"`
}

// TradingCalendar 返回严格升序且排除锚点的未来日线时间戳。
type TradingCalendar struct {
	AnchorTimestamp  int64   `json:"anchorTimestamp"`
	FutureTimestamps []int64 `json:"futureTimestamps"`
}
