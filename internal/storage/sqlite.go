// Package storage 实现 SQLite 只读查询，同一请求的元数据和日期在同一事务读取。
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"kcq-calendar-connector/internal/calendar"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
)

// snapshotColumns 与扫描顺序共用，避免不同查询对字段排序产生分歧。
const snapshotColumns = "exchange,start_date,end_date,source,source_version,source_url,generated_at,revision,row_count,override_count"

// Store 持有受限大小的只读数据库连接池。
type Store struct{ db *sql.DB }

// Open 打开已有数据库并检查版本；不自动创建空文件或修改表结构。
func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("calendar database unavailable; run scripts/sync_calendar.py first: %w", err)
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	if runtime.GOOS == "windows" {
		u.Path = "/" + u.Path
	}
	q := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)", "query_only(1)"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	store := &Store{db: db}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version != calendar.SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("unsupported calendar schema %d", version)
	}
	items, err := store.Snapshots(context.Background())
	if err != nil {
		db.Close()
		return nil, err
	}
	if len(items) == 0 {
		db.Close()
		return nil, fmt.Errorf("calendar database contains no snapshots")
	}
	return store, nil
}

// Close 释放数据库连接。
func (s *Store) Close() error { return s.db.Close() }

// Ping 验证数据库连接可用。
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// scanner 让 Row 和 Rows 共用元数据扫描逻辑。
type scanner interface{ Scan(...any) error }

// scanSnapshot 按唯一列定义读取快照元数据。
func scanSnapshot(row scanner) (calendar.Snapshot, error) {
	var item calendar.Snapshot
	err := row.Scan(&item.Exchange, &item.StartDate, &item.EndDate, &item.Source, &item.SourceVersion, &item.SourceURL, &item.GeneratedAt, &item.Revision, &item.RowCount, &item.OverrideCount)
	return item, err
}

// Snapshots 返回按市场代码排序的实际数据覆盖范围。
func (s *Store) Snapshots(ctx context.Context) ([]calendar.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+snapshotColumns+" FROM calendar_snapshots ORDER BY exchange")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]calendar.Snapshot, 0)
	for rows.Next() {
		item, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// snapshot 在查询所属事务中检查市场是否导入以及日期是否落在覆盖范围内。
func snapshot(ctx context.Context, tx *sql.Tx, exchange, start, end string) (calendar.Snapshot, error) {
	item, err := scanSnapshot(tx.QueryRowContext(ctx, "SELECT "+snapshotColumns+" FROM calendar_snapshots WHERE exchange = ?", exchange))
	if errors.Is(err, sql.ErrNoRows) {
		return item, calendar.Unsupported("calendar for %s has not been imported", exchange)
	}
	if err != nil {
		return item, err
	}
	if start < item.StartDate || end > item.EndDate {
		return item, calendar.Unsupported("requested dates are outside %s coverage [%s, %s]", exchange, item.StartDate, item.EndDate)
	}
	return item, nil
}

// Range 在同一个读取事务中返回元数据、日期和人工修订说明。
func (s *Store) Range(ctx context.Context, exchange, start, end string, openOnly bool) (calendar.DaysResult, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return calendar.DaysResult{}, err
	}
	defer tx.Rollback()
	meta, err := snapshot(ctx, tx, exchange, start, end)
	if err != nil {
		return calendar.DaysResult{}, err
	}
	query := `SELECT d.date,d.is_open,COALESCE(o.reason,''),COALESCE(o.source_url,'') FROM calendar_days d LEFT JOIN calendar_overrides o ON o.exchange=d.exchange AND o.date=d.date WHERE d.exchange=? AND d.date>=? AND d.date<=?`
	if openOnly {
		query += " AND d.is_open=1"
	}
	rows, err := tx.QueryContext(ctx, query+" ORDER BY d.date", exchange, start, end)
	if err != nil {
		return calendar.DaysResult{}, err
	}
	defer rows.Close()
	items := make([]calendar.Day, 0)
	for rows.Next() {
		var item calendar.Day
		if err := rows.Scan(&item.Date, &item.IsOpen, &item.OverrideReason, &item.OverrideSourceURL); err != nil {
			return calendar.DaysResult{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return calendar.DaysResult{}, err
	}
	return calendar.DaysResult{Snapshot: meta, Days: items}, tx.Commit()
}

// Next 使用开市日期索引获取排他锚点后的前缀，不在数据库末尾猜测日期。
func (s *Store) Next(ctx context.Context, exchange, after string, count int) (calendar.NextResult, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return calendar.NextResult{}, err
	}
	defer tx.Rollback()
	meta, err := snapshot(ctx, tx, exchange, after, after)
	if err != nil {
		return calendar.NextResult{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT date FROM calendar_days WHERE exchange=? AND is_open=1 AND date>? ORDER BY date LIMIT ?", exchange, after, count)
	if err != nil {
		return calendar.NextResult{}, err
	}
	defer rows.Close()
	dates := make([]string, 0, count)
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			return calendar.NextResult{}, err
		}
		dates = append(dates, date)
	}
	if err := rows.Err(); err != nil {
		return calendar.NextResult{}, err
	}
	return calendar.NextResult{Snapshot: meta, Dates: dates, Complete: len(dates) == count}, tx.Commit()
}
