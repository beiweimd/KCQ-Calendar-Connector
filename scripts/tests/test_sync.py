"""使用真实裁剪样本验证导入完整性、修订来源和 SQLite 事务回滚。"""

from __future__ import annotations

from copy import deepcopy
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sync_calendar import (
    ROOT,
    Snapshot,
    apply_overrides,
    date_range,
    load_markets,
    parse_date,
    validate_days,
    write_snapshots,
)


def reference_snapshots() -> list[Snapshot]:
    """Go/Python 测试使用同一份实际采集样本。"""
    content = json.loads(
        (ROOT / "internal/testsupport/testdata/reference.json").read_text(
            encoding="utf-8"
        )
    )
    markets = {
        item["exchange"]: item for item in load_markets([], "exchange_calendars")
    }
    return [
        Snapshot(
            markets[item["snapshot"]["exchange"]],
            [(row["date"], int(row["isOpen"])) for row in item["days"]],
            item["snapshot"]["sourceVersion"],
            [],
        )
        for item in content["snapshots"]
    ]


class SyncTests(unittest.TestCase):
    """同步失败不能破坏已经可用的数据库。"""

    def test_reject_partial_and_invalid_rows(self):
        """缺日期、重复日期和非 0/1 标记必须在写库前失败。"""
        expected = ["2026-11-25", "2026-11-26"]
        for rows in [
            [],
            [(expected[0], 1)],
            [(expected[0], 1)] * 2,
            [(expected[0], 1), (expected[1], 2)],
        ]:
            with self.assertRaises(ValueError):
                validate_days(rows, expected)

    def test_closed_interval_is_valid(self):
        """完整覆盖周末的全休市区间也是合法数据，不能误判为上游异常。"""
        dates = ["2026-11-28", "2026-11-29"]
        validate_days([(day, 0) for day in dates], dates)

    def test_atomic_rollback(self):
        """后一个市场触发数据库错误时，先写入市场的改动也必须回滚。"""
        snapshots = reference_snapshots()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "calendar.db"
            write_snapshots(path, snapshots)
            with closing(sqlite3.connect(path)) as db:
                before = db.execute(
                    "SELECT * FROM calendar_snapshots ORDER BY exchange"
                ).fetchall()
                db.execute(
                    "CREATE TRIGGER reject_xnys BEFORE INSERT ON calendar_days WHEN NEW.exchange='XNYS' BEGIN SELECT RAISE(ABORT,'test transactional failure'); END"
                )
            changed = deepcopy(snapshots)
            changed[0].days[0] = (changed[0].days[0][0], 1 - changed[0].days[0][1])
            with self.assertRaises(sqlite3.IntegrityError):
                write_snapshots(path, changed)
            with closing(sqlite3.connect(path)) as db:
                self.assertEqual(
                    before,
                    db.execute(
                        "SELECT * FROM calendar_snapshots ORDER BY exchange"
                    ).fetchall(),
                )
                self.assertEqual(
                    snapshots[0].days[0][1],
                    db.execute(
                        "SELECT is_open FROM calendar_days WHERE exchange=? AND date=?",
                        (snapshots[0].market["exchange"], snapshots[0].days[0][0]),
                    ).fetchone()[0],
                )

    def test_override_is_auditable(self):
        """人工调整保存理由和来源，且进入数据版本计算。"""
        snapshots = reference_snapshots()
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            overrides = directory / "overrides.json"
            overrides.write_text(
                json.dumps(
                    [
                        {
                            "exchange": "SSE",
                            "date": "2026-11-26",
                            "isOpen": False,
                            "reason": "测试人工修订存储，不代表真实休市",
                            "sourceUrl": "https://example.com/test-reference",
                        }
                    ]
                ),
                encoding="utf-8",
            )
            apply_overrides(snapshots, overrides)
            path = directory / "calendar.db"
            write_snapshots(path, snapshots)
            with closing(sqlite3.connect(path)) as db:
                self.assertEqual(
                    0,
                    db.execute(
                        "SELECT is_open FROM calendar_days WHERE exchange='SSE' AND date='2026-11-26'"
                    ).fetchone()[0],
                )
                self.assertEqual(
                    1,
                    db.execute(
                        "SELECT override_count FROM calendar_snapshots WHERE exchange='SSE'"
                    ).fetchone()[0],
                )
                self.assertIn(
                    "测试人工修订",
                    db.execute("SELECT reason FROM calendar_overrides").fetchone()[0],
                )

    def test_unknown_schema_preserved(self):
        """未知版本数据库不能被同步工具自动降级。"""
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "calendar.db"
            with closing(sqlite3.connect(path)) as db:
                db.execute("PRAGMA user_version=99")
            with self.assertRaises(ValueError):
                write_snapshots(path, reference_snapshots())
            with closing(sqlite3.connect(path)) as db:
                self.assertEqual(99, db.execute("PRAGMA user_version").fetchone()[0])

    def test_invalid_market_and_date(self):
        """不支持的市场和非 ISO 日期不能悄悄转换为默认值。"""
        with self.assertRaises(ValueError):
            load_markets(["FUTURES"])
        with self.assertRaises(ValueError):
            parse_date("2026-1-01")
        self.assertEqual(
            ["2026-02-28", "2026-03-01"],
            date_range(parse_date("2026-02-28"), parse_date("2026-03-01")),
        )

    def test_explicit_source_selection(self):
        """A 股离线规则来源必须显式选择，来源元数据随选择改变。"""
        default = load_markets(["SSE"])[0]
        rules = load_markets(["SSE"], "exchange_calendars")[0]
        self.assertEqual("baostock", default["source"])
        self.assertEqual("exchange_calendars", rules["source"])
        self.assertEqual("XSHG", rules["calendar"])


if __name__ == "__main__":
    unittest.main()
