// 本文件验证生产仓库确实只读，并能识别损坏或不兼容的数据库。
package storage

import (
	"context"
	"database/sql"
	"kcq-calendar-connector/internal/testsupport"
	"path/filepath"
	"testing"
)

// TestConcurrentSnapshotRead 验证更新事务进行时，查询不会混读新元数据和旧日期。
func TestConcurrentSnapshotRead(t *testing.T) {
	path := testsupport.Database(t)
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	writer, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		for n := 0; n < 30; n++ {
			tx, err := writer.Begin()
			if err != nil {
				done <- err
				return
			}
			open, revision := n%2 == 0, "test-closed"
			if open {
				revision = "test-open"
			}
			if _, err = tx.Exec("UPDATE calendar_snapshots SET revision=? WHERE exchange='XNYS'", revision); err == nil {
				_, err = tx.Exec("UPDATE calendar_days SET is_open=? WHERE exchange='XNYS' AND date='2026-11-25'", open)
			}
			if err != nil {
				tx.Rollback()
				done <- err
				return
			}
			if err = tx.Commit(); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	defer func() {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for n := 0; n < 100; n++ {
		result, err := store.Range(context.Background(), "XNYS", "2026-11-25", "2026-11-25", false)
		if err != nil {
			t.Fatal(err)
		}
		want := result.Snapshot.Revision != "test-closed"
		if len(result.Days) != 1 || result.Days[0].IsOpen != want {
			t.Fatalf("mixed snapshot: %+v", result)
		}
	}
}

// TestReadOnly 验证服务连接拒绝写入真实 SQLite。
func TestReadOnly(t *testing.T) {
	s, err := Open(testsupport.Database(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec("DELETE FROM calendar_days"); err == nil {
		t.Fatal("read-only connection accepted writes")
	}
}

// TestMissingAndUnknownSchema 验证缺失文件不会自动创建，未知版本不会被读取。
func TestMissingAndUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if _, err := Open(path); err == nil {
		t.Fatal("accepted missing database")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("accepted unknown database schema")
	}
}
