"""以本地数据库副本启动编译后的服务，验证真实 HTTP 和不停机更新，结束后清理自建进程。"""

from __future__ import annotations

import argparse
from contextlib import closing
from datetime import datetime, timezone
import json
from pathlib import Path
import queue
import sqlite3
import subprocess
import sys
import tempfile
import threading
import urllib.error
import urllib.request

from sync_calendar import ROOT, Snapshot, load_markets, write_snapshots


def query(base: str, path: str, body: dict | None = None, status: int = 200):
    """发送真实 HTTP 请求并检查状态、envelope 与请求标识。"""
    request = urllib.request.Request(
        base + path,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
    )
    try:
        response = urllib.request.urlopen(request, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        payload = json.loads(response.read())
        if response.status != status:
            raise AssertionError(
                f"{path}: expected {status}, got {response.status}: {payload}"
            )
        if (
            not payload.get("requestId")
            or response.headers["X-Request-ID"] != payload["requestId"]
        ):
            raise AssertionError("HTTP request ID mismatch")
        return payload.get("data") if status == 200 else payload["error"]


def snapshots_from_db(path: Path) -> list[Snapshot]:
    """读取数据库的完整真实记录，供事务重写验证使用，不引入新的数据来源。"""
    markets = {item["exchange"]: item for item in load_markets([])}
    result = []
    with closing(sqlite3.connect(path)) as db:
        db.row_factory = sqlite3.Row
        for meta in db.execute("SELECT * FROM calendar_snapshots ORDER BY exchange"):
            market = dict(markets[meta["exchange"]])
            if market["source"] != meta["source"]:
                market["calendar"] = market["alternateCalendars"][meta["source"]]
            market.update(source=meta["source"], sourceUrl=meta["source_url"])
            days = [
                (row[0], row[1])
                for row in db.execute(
                    "SELECT date,is_open FROM calendar_days WHERE exchange=? ORDER BY date",
                    (meta["exchange"],),
                )
            ]
            overrides = [
                {
                    "exchange": meta["exchange"],
                    "date": row[0],
                    "reason": row[1],
                    "sourceUrl": row[2],
                    "isOpen": bool(row[3]),
                }
                for row in db.execute(
                    "SELECT o.date,o.reason,o.source_url,d.is_open FROM calendar_overrides o JOIN calendar_days d USING(exchange,date) WHERE o.exchange=? ORDER BY o.date",
                    (meta["exchange"],),
                )
            ]
            result.append(Snapshot(market, days, meta["source_version"], overrides))
    return result


def main() -> int:
    """使用临时副本完成验证，不修改用户正在使用的数据库。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "bin/calendar-server.exe")
    parser.add_argument("--db", type=Path, default=ROOT / "data/calendar.db")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory() as directory:
        database = Path(directory) / "calendar.db"
        with (
            closing(
                sqlite3.connect(args.db.resolve().as_uri() + "?mode=ro", uri=True)
            ) as original,
            closing(sqlite3.connect(database)) as target,
        ):
            original.backup(target)
        flags = subprocess.CREATE_NO_WINDOW if sys.platform == "win32" else 0
        process = subprocess.Popen(
            [
                str(args.binary.resolve()),
                "--db",
                str(database),
                "--addr",
                "127.0.0.1:0",
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            creationflags=flags,
        )
        logs: queue.Queue[str] = queue.Queue()

        def read_logs():
            """异步读取启动日志，防止启动失败时验证无限等待。"""
            for line in process.stderr:
                logs.put(line)

        reader = threading.Thread(target=read_logs, daemon=True)
        reader.start()
        checks = []
        try:
            started = json.loads(logs.get(timeout=15))
            base = "http://" + started["address"]
            assert query(base, "/healthz")["status"] == "ok"
            checks.append("real HTTP process and read-only SQLite")
            markets = query(base, "/api/v1/calendars")
            assert {item["market"]["exchange"] for item in markets} == {
                "SSE",
                "SZSE",
                "XHKG",
                "XNYS",
                "XNAS",
            }
            for item in markets:
                assert item["market"]["source"] == item["snapshot"]["source"]
            checks.append("five markets with actual source metadata")
            for exchange, day, expected in [
                ("SSE", "2026-10-01", False),
                ("HK", "2026-02-17", False),
                ("NYSE", "2026-10-01", True),
                ("NASDAQ", "2026-11-26", False),
            ]:
                result = query(
                    base, f"/api/v1/calendars/{exchange}/days?start={day}&end={day}"
                )
                assert result["days"][0]["isOpen"] == expected
            checks.append("Chinese, Hong Kong and US holiday differences")
            cn = query(
                base, "/api/v1/calendars/SH/days?start=2026-01-01&end=2026-12-31"
            )
            assert (
                len(cn["days"]) == 365
                and sum(row["isOpen"] for row in cn["days"]) == 242
            )
            checks.append("full 2026 A-share dates and 242 scheduled sessions")
            after = query(base, "/api/v1/calendars/NYSE/next?after=2026-11-25&count=2")
            assert after["dates"] == ["2026-11-27", "2026-11-30"] and after["complete"]
            checks.append("exclusive anchor, Thanksgiving and weekend skipping")
            anchor = int(
                datetime(2026, 10, 30, 13, 30, tzinfo=timezone.utc).timestamp() * 1000
            )
            body = {
                "sourceId": "test-connector",
                "instrument": {"symbol": "AAPL", "exchange": "NASDAQ"},
                "period": "daily",
                "adjustment": "none",
                "barAggregation": "original",
                "anchorTimestamp": anchor,
                "count": 1,
            }
            future = query(base, "/api/v1/market-data/trading-calendar", body)
            assert future["futureTimestamps"] == [
                int(
                    datetime(2026, 11, 2, 14, 30, tzinfo=timezone.utc).timestamp()
                    * 1000
                )
            ]
            checks.append("KCQ V1 timestamps across US daylight saving change")
            for path in [
                "/api/v1/calendars/SSE/days?start=2027-01-01&end=2027-01-02",
                "/api/v1/calendars/FUTURES/next?after=2026-10-28&count=1",
            ]:
                assert query(base, path, status=422)["code"] == "UNSUPPORTED_CAPABILITY"
            body["period"] = "5min"
            assert (
                query(base, "/api/v1/market-data/trading-calendar", body, status=422)[
                    "code"
                ]
                == "UNSUPPORTED_CAPABILITY"
            )
            checks.append("explicit errors for uncovered dates, futures and minutes")
            before = query(base, "/api/v1/calendars")
            write_snapshots(database, snapshots_from_db(database))
            after_update = query(base, "/api/v1/calendars")
            assert process.poll() is None
            assert (
                before[0]["snapshot"]["generatedAt"]
                != after_update[0]["snapshot"]["generatedAt"]
            )
            assert [row["snapshot"]["revision"] for row in before] == [
                row["snapshot"]["revision"] for row in after_update
            ]
            checks.append("atomic refresh visible without restarting the Go process")
            print(
                json.dumps(
                    {
                        "status": "passed",
                        "checks": checks,
                        "markets": [item["snapshot"] for item in markets],
                    },
                    ensure_ascii=False,
                    indent=2,
                )
            )
        finally:
            process.terminate()
            process.wait(timeout=10)
            reader.join(timeout=2)
            process.stderr.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
