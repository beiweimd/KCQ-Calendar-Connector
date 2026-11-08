# KCQ Calendar Connector

独立的 Go 交易日历查询服务。Python 离线采集 / 生成日历并写入 SQLite；Go 仅查询已有数据库，运行时不依赖上游 API。

## 覆盖范围

| 市场代码 | 别名 | 数据来源 |
|---|---|---|
| SSE | SH、SHSE、XSHG | Baostock A 股日历 |
| SZSE | SZ、XSHE | Baostock A 股日历 |
| XHKG | HK、HKEX、SEHK | exchange_calendars / XHKG |
| XNYS | NYSE | exchange_calendars / XNYS |
| XNAS | NASDAQ | exchange_calendars / XNYS（该库的 Nasdaq 日历别名） |

第一版提供**市场级、日期粒度的计划交易日历**，以及 KCQ `daily/original` 未来槽位。股票、ETF 等可以复用所在市场的日历，但不表达单个证券停牌、上市退市、合约到期。未实现期货品种夜盘、分钟周期、集合竞价、盘前盘后和半日市具体时段；半日市在日期层面仍是开市。其他资产和市场必须显式增加来源与规则，不能仅按周一到周五推测。

默认数据范围为 2015 年至当前年份年底。**范围代表实际导入的区间，不代表任意未来年份已经被官方确认。** Baostock 未返回完整区间时，同步会失败，保留旧快照；港美数据是开源库规则生成的计划日历，特殊变更须更新库或用有来源的 overrides 修订。

## 本地运行

需要 Go 1.25+、Python 3.12+、uv。在本目录执行：

```powershell
uv sync --frozen
uv run --frozen python scripts/sync_calendar.py --start 2015-01-01 --end 2026-12-31
go run ./cmd/server
```

Baostock 暂时不可达，或希望全部离线生成时，可以**显式选择** A 股的 `exchange_calendars / XSHG` 规则日历：

```powershell
uv run --frozen python scripts/sync_calendar.py --cn-source exchange_calendars --start 2015-01-01 --end 2026-12-31
```

这不是联网失败后的自动回退：未指定时仍然只使用 Baostock，失败即停止。实际来源写入快照并随接口返回，不会将规则生成的数据标记为 Baostock 数据。

默认监听 `127.0.0.1:8094`，读取 `data/calendar.db`。第一次必须先同步数据；服务不会创建空数据库。仅部署 Go 程序和 SQLite 也能查询，无需 Python：

```powershell
go build -o bin/calendar-server.exe ./cmd/server
.\bin\calendar-server.exe --db data/calendar.db --addr 127.0.0.1:8094
```

本地已编译时也可运行 `./start.ps1`，脚本使用项目目录下的数据库绝对路径。

| 配置 | CLI 参数 | 默认值 |
|---|---|---|
| CALENDAR_ADDR | --addr | 127.0.0.1:8094 |
| CALENDAR_DB_PATH | --db | data/calendar.db |
| CALENDAR_ALLOWED_ORIGINS | --origins | http://localhost:5173,http://127.0.0.1:5173 |

端口和路径可按部署环境修改。浏览器直连其他开发端口时，显式增加对应 origin；Connector 之间的服务调用不需要 CORS。

## 查询接口

机器可读契约见 [docs/openapi.yaml](docs/openapi.yaml)。

成功返回 `{ "data": ..., "requestId": "..." }`，错误返回 `{ "error": { "code": "...", "message": "..." }, "requestId": "..." }`。同时返回 `X-Request-ID` 响应头。

```text
GET /healthz
GET /api/v1/calendars
GET /api/v1/calendars/HK/days?start=2026-10-01&end=2026-10-10
GET /api/v1/calendars/NYSE/days?start=2026-11-01&end=2026-11-30&openOnly=true
GET /api/v1/calendars/SSE/next?after=2026-10-28&count=10
POST /api/v1/market-data/trading-calendar
```

`days` 包含起止两天，每次最多 3660 个自然日，默认返回开市及休市日期。`next` 的 `after` 为排他日期，`count` 为 1–4096；覆盖末尾可以少于请求数量，必须检查 `complete`。未知市场、未导入市场、超出覆盖范围和未实现周期均明确返回错误，不把空数组伪装成已确认的休市。

市场列表和查询结果包含来源、版本、覆盖起止日期、生成时间、内容 SHA-256、记录数和人工修订数。人工修订日期额外返回理由与来源链接。

## KCQ 接入边界

本服务是**日历专用服务**，不是提供 K 线和品种搜索的完整行情 Provider，不能直接当作一个 bars 数据源注册。推荐链路：

```text
KCQ → 已有行情 Connector 的 trading-calendar → 本服务 → SQLite
```

使用包含日历转发功能的 Baostock-Tradingview-Connector 版本。启动前设置 `KCQ_CALENDAR_URL=http://127.0.0.1:8094`，KCQ 仍连接行情服务的 `8000` 端口。重新探测数据源并搜索选择品种后，Baostock 沪深品种声明日历能力，`daily/original` 请求转发至本服务。保留原始 `sourceId` 和品种代码，并固定沪深市场映射与上海日线日期口径；不需要修改 KCQ Core。北交所、TradingView 与 finshare 暂未接入，行情 bars 实现不变。

```json
{
  "sourceId": "baostock",
  "instrument": {"symbol": "600519", "exchange": "SH"},
  "period": "daily",
  "adjustment": "none",
  "barAggregation": "original",
  "anchorTimestamp": 1793116800000,
  "count": 10
}
```

V1 返回 `anchorTimestamp` 和严格升序的 `futureTimestamps`（UTC 毫秒）。价格复权不改变交易日期，因此 `adjustment` 不参与日历计算。

**必须匹配上游日线时间戳口径：** 默认按照市场时区解释锚点日期，保持当地时分秒；跨美股夏令时会重新计算 UTC 偏移。如果上游用 UTC 零点标记交易日，应在 `instrument.providerRef` 中显式传 `"calendarTimeZone": "UTC"`。如果上游 exchange 不够明确，传 `"calendarExchange": "XNYS"` 等规范代码。服务不从股票代码猜测市场，也不把美股 UTC 日期和纽约当地日期混用。

V1 在覆盖末尾按 KCQ 协议返回可用前缀；需要完整数量的调用方应检查长度，并通过市场列表检查覆盖截止日。

## 数据更新和人工修订

```powershell
# 更新选中市场的完整快照；未选择的市场保持原样。
uv run --frozen python scripts/sync_calendar.py --markets XHKG XNYS XNAS --start 2015-01-01 --end 2026-12-31
```

采集、完整性校验全部成功后，选中市场在**一个数据库事务**中替换。请求区间就是替换后的覆盖区间，不是追加范围；更新时保持所需历史起点。Go 通过只读连接访问数据库，Python 原地事务更新；不需要重启 Go。不要在运行期间用文件替换方式交换数据库。

`config/overrides.json` 默认空数组，特殊调整按以下字段填写真实记录：`exchange`、`date`、`isOpen`、`reason`、`sourceUrl`。要求有明确原因和来源链接，禁止通过无来源的手动修改绕过数据维护。每次同步都会重新应用区间内的 overrides。

年度基础安排发布后更新全年；年中定期检查交易所公告和来源库变更，有调整及时同步。服务不自行定时联网；可用系统任务调度器运行同步命令。

数据库默认忽略于 Git，只保留在本地。Go 部署时携带 SQLite 文件；更新完成、采集进程关闭后再复制，确保没有尚未 checkpoint 的 WAL 内容。`go:embed` 不用于可更新的日历数据。

## 目录和验证

```text
cmd/server/                  启动装配和生命周期
internal/calendar/           领域契约、市场配置、日期与 V1 适配
internal/storage/schema.sql  唯一表结构
internal/storage/            SQLite 只读实现
internal/httpapi/            路由、envelope、CORS
internal/testsupport/        实际采集的离线测试样本
scripts/                    数据采集、原子更新和验证脚本
config/overrides.json        有来源的日历例外
data/calendar.db             本地生成数据库
```

```powershell
go test ./...
go vet ./...
uv run --frozen python -m unittest discover -s scripts/tests
uv run --frozen python scripts/verify_local.py
```

`verify_local.py` 使用数据库的临时副本启动编译后的真实服务，验证 HTTP、市场差异、V1 时区和不停机更新，结束后自动关闭该验证进程。它不会修改原数据库。

测试覆盖真实 SQLite 只读行为、市场差异、感恩节、夏令时、UTC 锚点、覆盖末尾、输入错误、CORS、更新失败回滚及修订记录。测试不调用收费 API。网络采集由同步命令显式执行。

### Baostock 接入验证

先重新编译本服务，再用已有 Connector 的 Python 环境运行真实 HTTP 链路验证：

```powershell
go build -o bin/calendar-server.exe ./cmd/server
..\Baostock-Tradingview-Connector\.venv\Scripts\python.exe scripts/verify_baostock.py --connector ../Baostock-Tradingview-Connector
```

脚本使用数据库副本和临时端口，验证沪深日线跳过国庆与周末、未支持周期、覆盖末尾和上游停机；结束后清理自建服务。关闭 Connector 的行情目录启动预热，不请求 Baostock 行情。
可加 `--kcq ../KLineChartQuant --tsx-cli ../KLineChartQuant/node_modules/tsx/dist/cli.mjs`，用指定 KCQ 源码的实际 HTTP Transport、Provider 和图表 DataBuffer 验证未来槽位消费。源/品种能力直接来自 Connector 生产生成函数，锚点取自生产日期转换器；此验证不包含真实行情登录或浏览器 UI 操作。

## 来源与许可

- Baostock：<http://www.baostock.com>，使用其 `query_trade_dates`。
- exchange_calendars：<https://github.com/gerrymanoim/exchange_calendars>，Apache-2.0；依赖版本由 `uv.lock` 固定。
- 核对依据：[港交所日历](https://www.hkex.com.hk/News/HKEX-Calendar)、[NYSE](https://www.nyse.com/trade/hours-calendars)、[Nasdaq](https://nasdaqtrader.com/Trader.aspx?id=Calendar)。

代码、库许可和数据分发许可分别处理。当前数据库用于本地开发；没有把可访问 API 等同于数据再分发授权，也未把第三方数据重新声明为本项目自有数据。
