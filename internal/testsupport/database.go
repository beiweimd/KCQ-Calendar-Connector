// Package testsupport 从真实采集的裁剪样本构建临时数据库，不替换生产仓库实现。
package testsupport

import (
	"database/sql"
	"encoding/json"
	"kcq-calendar-connector/internal/calendar"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Reference 将真实采集的元数据和日期作为同一份测试样本。
type Reference struct {
	Snapshots []struct {
		Snapshot calendar.Snapshot `json:"snapshot"`
		Days     []calendar.Day    `json:"days"`
	} `json:"snapshots"`
}

// SourceRoot 使用源码位置寻找共用表结构和已提交测试样本。
func SourceRoot() string { _, file, _, _ := runtime.Caller(0); return filepath.Dir(filepath.Dir(file)) }

// Database 在每个测试独立目录创建真实 SQLite，返回其绝对路径。
func Database(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "calendar.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := os.ReadFile(filepath.Join(SourceRoot(), "storage", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(SourceRoot(), "testsupport", "testdata", "reference.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reference Reference
	if err := json.Unmarshal(content, &reference); err != nil {
		t.Fatal(err)
	}
	for _, item := range reference.Snapshots {
		meta := item.Snapshot
		if _, err := db.Exec("INSERT INTO calendar_snapshots VALUES (?,?,?,?,?,?,?,?,?,?)", meta.Exchange, meta.StartDate, meta.EndDate, meta.Source, meta.SourceVersion, meta.SourceURL, meta.GeneratedAt, meta.Revision, len(item.Days), 0); err != nil {
			t.Fatal(err)
		}
		for _, day := range item.Days {
			if _, err := db.Exec("INSERT INTO calendar_days VALUES (?,?,?)", meta.Exchange, day.Date, day.IsOpen); err != nil {
				t.Fatal(err)
			}
		}
	}
	return path
}
