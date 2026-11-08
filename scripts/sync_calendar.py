"""采集真实交易日期并以单个事务更新 SQLite；网络失败或数据不完整时保留旧快照。"""

from __future__ import annotations

import argparse
from contextlib import redirect_stdout
from dataclasses import dataclass
from datetime import date, datetime, timedelta, timezone
import hashlib
from importlib.metadata import version
import json
from pathlib import Path
import socket
import sqlite3
import sys

ROOT = Path(__file__).resolve().parents[1]
CATALOG = ROOT / "internal/calendar/markets.json"
SCHEMA = ROOT / "internal/storage/schema.sql"
SCHEMA_VERSION = 1
DEFAULT_START = "2015-01-01"
DATE_FORMAT = "%Y-%m-%d"


@dataclass
class Snapshot:
    """一个市场的完整自然日期序列、来源信息和人工覆盖记录。"""

    market: dict
    days: list[tuple[str, int]]
    source_version: str
    overrides: list[dict]


def parse_date(value: str) -> date:
    """只接受 YYYY-MM-DD 格式的有效日期。"""
    result = date.fromisoformat(value)
    if result.isoformat() != value:
        raise ValueError("日期必须使用 YYYY-MM-DD 格式")
    return result


def date_range(start: date, end: date) -> list[str]:
    """生成包含两个边界的完整自然日期序列。"""
    return [
        (start + timedelta(days=i)).isoformat() for i in range((end - start).days + 1)
    ]


def load_markets(selected: list[str], cn_source: str = "baostock") -> list[dict]:
    """使用 Go 共用配置解析市场代码，不为未知市场猜测来源。"""
    config = json.loads(CATALOG.read_text(encoding="utf-8"))
    markets = config["markets"]
    for item in markets:
        if "alternateCalendars" in item and cn_source != item["source"]:
            if cn_source not in item["alternateCalendars"]:
                raise ValueError(f"不支持的 A 股来源：{cn_source}")
            item["source"] = cn_source
            item["calendar"] = item["alternateCalendars"][cn_source]
        item["sourceUrl"] = config["sources"][item["source"]]["url"]
    lookup = {
        key: item for item in markets for key in [item["exchange"], *item["aliases"]]
    }
    if not selected:
        return markets
    result = []
    for name in selected:
        key = name.upper()
        if key not in lookup:
            raise ValueError(f"不支持市场：{name}")
        if lookup[key] not in result:
            result.append(lookup[key])
    return result


def validate_days(rows: list[tuple[str, int]], expected: list[str]) -> None:
    """拒绝空响应、日期缺口、重复记录和非布尔开市标记。"""
    if [item[0] for item in rows] != expected or not rows:
        raise ValueError(
            "上游日历未完整覆盖请求区间，拒绝替换数据库；请缩小到已公布年份"
        )
    if any(type(flag) is not int or flag not in (0, 1) for _, flag in rows):
        raise ValueError("上游返回了无效的开市标记")


def fetch_baostock(start: str, end: str) -> list[tuple[str, int]]:
    """匿名查询 A 股日历，仅在需要该来源时建立网络连接。"""
    import baostock as bs

    socket.setdefaulttimeout(20)
    with redirect_stdout(sys.stderr):
        login = bs.login()
        if login.error_code != "0":
            raise RuntimeError(
                f"Baostock 登录失败：{login.error_code} {login.error_msg}"
            )
        try:
            response = bs.query_trade_dates(start_date=start, end_date=end)
            rows = []
            while response.error_code == "0" and response.next():
                value = response.get_row_data()
                if len(value) != 2 or value[1] not in ("0", "1"):
                    raise ValueError("Baostock 返回了不符合日历契约的数据")
                rows.append((value[0], int(value[1])))
            if response.error_code != "0":
                raise RuntimeError(
                    f"Baostock 查询失败：{response.error_code} {response.error_msg}"
                )
            return rows
        finally:
            bs.logout()


def collect(markets: list[dict], start: date, end: date) -> list[Snapshot]:
    """按来源复用采集结果，所有市场验证完成后才允许写库。"""
    expected = date_range(start, end)
    cache: dict[tuple[str, str], list[tuple[str, int]]] = {}
    snapshots = []
    for market in markets:
        key = (market["source"], market["calendar"])
        if key not in cache:
            if market["source"] == "baostock":
                rows = fetch_baostock(start.isoformat(), end.isoformat())
            elif market["source"] == "exchange_calendars":
                import exchange_calendars as xcals

                # 按完整年份构造规则，短区间即使全部休市也能得到有效的自然日期标记。
                cal = xcals.get_calendar(
                    market["calendar"],
                    start=f"{start.year}-01-01",
                    end=f"{end.year}-12-31",
                )
                sessions = {stamp.strftime(DATE_FORMAT) for stamp in cal.sessions}
                rows = [(day, int(day in sessions)) for day in expected]
            else:
                raise ValueError(f"没有实现的日历来源：{market['source']}")
            validate_days(rows, expected)
            cache[key] = rows
        snapshots.append(
            Snapshot(market, list(cache[key]), version(market["source"]), [])
        )
    return snapshots


def apply_overrides(snapshots: list[Snapshot], path: Path) -> None:
    """对已采集区间应用有理由和来源链接的显式修订，并保留审计记录。"""
    entries = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(entries, list):
        raise ValueError("overrides 必须为 JSON 数组")
    by_market = {item.market["exchange"]: item for item in snapshots}
    known_markets = {item["exchange"] for item in load_markets([])}
    seen = set()
    for item in entries:
        exchange = item["exchange"]
        if exchange not in known_markets:
            raise ValueError(f"修订必须使用规范市场代码：{exchange}")
        day = parse_date(item["date"]).isoformat()
        if (
            type(item["isOpen"]) is not bool
            or not item["reason"].strip()
            or not item["sourceUrl"].startswith(("https://", "http://"))
        ):
            raise ValueError("每条修订必须有布尔 isOpen、原因和 HTTP 来源链接")
        if (exchange, day) in seen:
            raise ValueError(f"重复修订：{exchange} {day}")
        seen.add((exchange, day))
        if exchange not in by_market:
            continue
        snapshot = by_market[exchange]
        if snapshot.days[0][0] <= day <= snapshot.days[-1][0]:
            snapshot.days = [
                (value, int(item["isOpen"]) if value == day else flag)
                for value, flag in snapshot.days
            ]
            snapshot.overrides.append(item)


def write_snapshots(path: Path, snapshots: list[Snapshot]) -> list[dict]:
    """在同一个事务更新所有选中市场，任一写入失败将回滚全部快照。"""
    if not snapshots:
        raise ValueError("没有可写入的市场")
    for item in snapshots:
        validate_days(
            item.days,
            date_range(parse_date(item.days[0][0]), parse_date(item.days[-1][0])),
        )
    path.parent.mkdir(parents=True, exist_ok=True)
    connection = sqlite3.connect(path, timeout=10)
    try:
        existing_version = connection.execute("PRAGMA user_version").fetchone()[0]
        if existing_version not in (0, SCHEMA_VERSION):
            raise ValueError(f"不支持数据库版本 {existing_version}")
        connection.execute("PRAGMA journal_mode=WAL")
        connection.execute("PRAGMA foreign_keys=ON")
        connection.executescript(SCHEMA.read_text(encoding="utf-8"))
        generated_at = datetime.now(timezone.utc).isoformat()
        summary = []
        with connection:
            for snapshot in snapshots:
                market = snapshot.market
                exchange = market["exchange"]
                payload = json.dumps(
                    {
                        "days": snapshot.days,
                        "overrides": snapshot.overrides,
                        "source": market["source"],
                        "version": snapshot.source_version,
                    },
                    ensure_ascii=False,
                    separators=(",", ":"),
                )
                revision = hashlib.sha256(payload.encode("utf-8")).hexdigest()
                connection.execute(
                    "DELETE FROM calendar_overrides WHERE exchange=?", (exchange,)
                )
                connection.execute(
                    "DELETE FROM calendar_days WHERE exchange=?", (exchange,)
                )
                connection.execute(
                    """INSERT INTO calendar_snapshots VALUES (?,?,?,?,?,?,?,?,?,?)
                    ON CONFLICT(exchange) DO UPDATE SET start_date=excluded.start_date,end_date=excluded.end_date,
                    source=excluded.source,source_version=excluded.source_version,source_url=excluded.source_url,
                    generated_at=excluded.generated_at,revision=excluded.revision,row_count=excluded.row_count,
                    override_count=excluded.override_count""",
                    (
                        exchange,
                        snapshot.days[0][0],
                        snapshot.days[-1][0],
                        market["source"],
                        snapshot.source_version,
                        market["sourceUrl"],
                        generated_at,
                        revision,
                        len(snapshot.days),
                        len(snapshot.overrides),
                    ),
                )
                connection.executemany(
                    "INSERT INTO calendar_days VALUES (?,?,?)",
                    [(exchange, day, flag) for day, flag in snapshot.days],
                )
                connection.executemany(
                    "INSERT INTO calendar_overrides VALUES (?,?,?,?)",
                    [
                        (exchange, item["date"], item["reason"], item["sourceUrl"])
                        for item in snapshot.overrides
                    ],
                )
                summary.append(
                    {
                        "exchange": exchange,
                        "start": snapshot.days[0][0],
                        "end": snapshot.days[-1][0],
                        "rows": len(snapshot.days),
                        "openDays": sum(flag for _, flag in snapshot.days),
                        "revision": revision,
                    }
                )
        connection.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        return summary
    finally:
        connection.close()


def main() -> int:
    """解析显式日期区间，打印可供脚本消费的同步结果。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", type=Path, default=ROOT / "data/calendar.db")
    parser.add_argument("--start", default=DEFAULT_START)
    parser.add_argument("--end", default=f"{datetime.now(timezone.utc).year}-12-31")
    parser.add_argument("--markets", nargs="*", default=[])
    parser.add_argument(
        "--overrides", type=Path, default=ROOT / "config/overrides.json"
    )
    parser.add_argument(
        "--cn-source",
        choices=["baostock", "exchange_calendars"],
        default="baostock",
        help="显式选择 A 股来源，不会在联网失败后自动切换",
    )
    args = parser.parse_args()
    try:
        start, end = parse_date(args.start), parse_date(args.end)
        if start > end or start.year < 1991 or end.year > 2100:
            raise ValueError("请求区间必须有序且处于 1991 至 2100 年")
        snapshots = collect(load_markets(args.markets, args.cn_source), start, end)
        apply_overrides(snapshots, args.overrides)
        print(
            json.dumps(
                {
                    "database": str(args.db.resolve()),
                    "markets": write_snapshots(args.db, snapshots),
                },
                ensure_ascii=False,
                indent=2,
            )
        )
        return 0
    except Exception as error:
        print(f"日历同步失败：{error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
