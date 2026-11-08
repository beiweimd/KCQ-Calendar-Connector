-- Python 写入、Go 读取的唯一 SQLite 表结构；只在显式更新数据时初始化。
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS calendar_snapshots (
    exchange TEXT PRIMARY KEY,
    start_date TEXT NOT NULL,
    end_date TEXT NOT NULL,
    source TEXT NOT NULL,
    source_version TEXT NOT NULL,
    source_url TEXT NOT NULL,
    generated_at TEXT NOT NULL,
    revision TEXT NOT NULL,
    row_count INTEGER NOT NULL CHECK (row_count > 0),
    override_count INTEGER NOT NULL CHECK (override_count >= 0)
);
CREATE TABLE IF NOT EXISTS calendar_days (
    exchange TEXT NOT NULL REFERENCES calendar_snapshots(exchange),
    date TEXT NOT NULL,
    is_open INTEGER NOT NULL CHECK (is_open IN (0, 1)),
    PRIMARY KEY (exchange, date)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS calendar_open_days ON calendar_days(exchange, date) WHERE is_open = 1;
CREATE TABLE IF NOT EXISTS calendar_overrides (
    exchange TEXT NOT NULL,
    date TEXT NOT NULL,
    reason TEXT NOT NULL,
    source_url TEXT NOT NULL,
    PRIMARY KEY (exchange, date),
    FOREIGN KEY (exchange, date) REFERENCES calendar_days(exchange, date)
);
PRAGMA user_version = 1;
