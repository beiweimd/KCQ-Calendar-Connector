"""离线启动真实 Calendar 和 Baostock HTTP 路由，可选验证 KCQ Provider 与图表数据层。"""

from __future__ import annotations

import argparse
from contextlib import closing
import json
import os
from pathlib import Path
import queue
import socket
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
from urllib.error import HTTPError
from urllib.request import Request, urlopen


ROOT = Path(__file__).resolve().parents[1]


def query(base: str, payload: dict, expected_status: int = 200) -> dict:
    """经真实 HTTP 访问 Connector，核对 V1 envelope 和错误码。"""
    request = Request(
        base + "/api/v1/market-data/trading-calendar",
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json"},
    )
    try:
        response = urlopen(request, timeout=10)
    except HTTPError as error:
        response = error
    with response:
        body = json.loads(response.read())
        assert response.status == expected_status, body
        assert body.get("requestId"), body
        return body["data"] if expected_status == 200 else body["error"]


def main() -> int:
    """使用数据库副本及临时端口；不连接 Baostock 行情，不修改 KCQ 源码。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--connector", type=Path, required=True)
    parser.add_argument("--binary", type=Path, default=ROOT / "bin/calendar-server.exe")
    parser.add_argument("--db", type=Path, default=ROOT / "data/calendar.db")
    parser.add_argument("--kcq", type=Path)
    parser.add_argument("--tsx-cli", type=Path, help="已安装 tsx 的 dist/cli.mjs")
    args = parser.parse_args()
    if bool(args.kcq) != bool(args.tsx_cli):
        parser.error("--kcq and --tsx-cli must be provided together")
    sys.path.insert(0, str(args.connector.resolve()))
    import uvicorn
    from api.market_data_v1 import _baostock_capabilities, _instrument, _timestamp_ms
    from server import app

    with tempfile.TemporaryDirectory() as directory:
        temp = Path(directory)
        database = temp / "calendar.db"
        with closing(sqlite3.connect(args.db.resolve().as_uri() + "?mode=ro", uri=True)) as original:
            with closing(sqlite3.connect(database)) as target:
                original.backup(target)
        process = subprocess.Popen(
            [str(args.binary.resolve()), "--db", str(database), "--addr", "127.0.0.1:0"],
            stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True, encoding="utf-8",
            creationflags=subprocess.CREATE_NO_WINDOW if sys.platform == "win32" else 0,
        )
        logs: queue.Queue[str] = queue.Queue()

        def read_logs():
            """异步读取 Go 地址，避免启动失败时无期限等待。"""
            for line in process.stderr:
                logs.put(line)

        reader = threading.Thread(target=read_logs, daemon=True)
        reader.start()
        server = None
        worker = None
        listener = None
        old_url = os.environ.get("KCQ_CALENDAR_URL")
        checks = []
        try:
            os.environ["KCQ_CALENDAR_URL"] = "http://" + json.loads(logs.get(timeout=15))["address"]
            listener = socket.socket()
            listener.bind(("127.0.0.1", 0))
            base = f"http://127.0.0.1:{listener.getsockname()[1]}"
            # 仅关闭会触发行情联网的启动预热；仍使用生产 app、HTTP 路由和转发实现。
            server = uvicorn.Server(uvicorn.Config(app, lifespan="off", log_level="error"))
            worker = threading.Thread(target=server.run, kwargs={"sockets": [listener]}, daemon=True)
            worker.start()
            deadline = time.monotonic() + 15
            while not server.started:
                if not worker.is_alive() or time.monotonic() > deadline:
                    raise RuntimeError("Connector failed to start")
                time.sleep(0.05)
            cases = []
            for code, day, expected in [
                # 上交所 2026-09-17 公告：10/8 开市，10/10 虽调休上班仍为周末休市。
                ("sh.600000", "2026-09-30", ["2026-10-08", "2026-10-09", "2026-10-12"]),
                ("sz.000001", "2026-10-30", ["2026-11-02", "2026-11-03"]),
            ]:
                instrument = _instrument(code, code)
                payload = {
                    "sourceId": "baostock", "instrument": instrument,
                    "period": "daily", "adjustment": "none", "barAggregation": "original",
                    "anchorTimestamp": _timestamp_ms({"date": day}), "count": len(expected),
                }
                expected_stamps = [_timestamp_ms({"date": value}) for value in expected]
                result = query(base, payload)
                assert result == {"anchorTimestamp": payload["anchorTimestamp"], "futureTimestamps": expected_stamps}, result
                cases.append({"request": payload, "expected": expected_stamps})
            checks.append("real Connector -> Go -> SQLite skips National Day and weekends for SH/SZ")
            payload = cases[0]["request"]
            for changes in [{"period": "5min"}, {"barAggregation": "aligned"}, {"sourceId": "finshare"},
                            {"anchorTimestamp": _timestamp_ms({"date": "2035-01-01"})}]:
                assert query(base, {**payload, **changes}, 400)["code"] == "UNSUPPORTED_CAPABILITY"
            assert query(base, {**payload, "count": 0}, 400)["code"] == "INVALID_REQUEST"
            checks.append("unsupported period/source/range and invalid input retain V1 errors")
            with closing(sqlite3.connect(database)) as db:
                end = db.execute("SELECT end_date FROM calendar_snapshots WHERE exchange='SSE'").fetchone()[0]
            result = query(base, {**payload, "anchorTimestamp": _timestamp_ms({"date": end})})
            assert result["futureTimestamps"] == []
            checks.append("coverage end returns an empty available prefix without guessing dates")
            if args.kcq:
                kcq = args.kcq.resolve()
                fixture = temp / "integration.json"
                fixture.write_text(json.dumps({
                    "baseUrl": base, "kcqRoot": str(kcq),
                    "capabilities": _baostock_capabilities(), "cases": cases,
                }), encoding="utf-8")
                config = temp / "tsconfig.json"
                config.write_text(json.dumps({"compilerOptions": {"baseUrl": str(kcq), "paths": {
                    "@/*": ["packages/core/src/*"],
                    "@363045841yyt/klinechart-plot-contracts": ["packages/plot-contracts/src/index.ts"],
                }}}), encoding="utf-8")
                subprocess.run([
                    "node", str(args.tsx_cli.resolve()), "--tsconfig", str(config),
                    str(ROOT / "scripts/verify_kcq_calendar.mts"), str(fixture),
                ], check=True, timeout=60)
                checks.append("KCQ actual HTTP transport, Provider and DataBuffer consume future slots")
            process.terminate()
            process.wait(timeout=10)
            assert query(base, payload, 502)["code"] == "UPSTREAM_UNAVAILABLE"
            checks.append("stopped Calendar returns UPSTREAM_UNAVAILABLE through Connector")
            print(json.dumps({"status": "passed", "checks": checks}, ensure_ascii=False, indent=2))
        finally:
            if server:
                server.should_exit = True
            if worker:
                worker.join(timeout=10)
            if listener:
                listener.close()
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=10)
            reader.join(timeout=5)
            process.stderr.close()
            if old_url is None:
                os.environ.pop("KCQ_CALENDAR_URL", None)
            else:
                os.environ["KCQ_CALENDAR_URL"] = old_url
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
