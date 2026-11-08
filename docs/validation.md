# 本地验证记录

## 2026-11-08 主线复测

KCQ `1da6969d`、Baostock 上游 `d5aab7a` 加接入补丁。在此版本使用 `DataBuffer.mergePage()` 装入日线锚点。独立服务的 Go 测试、`go vet`、7 项 Python 测试和 8 项 HTTP 检查均通过。Baostock 的 28 项测试通过，真实 HTTP 联调验证了沪深交易日期、范围末尾的可用前缀、错误响应、图表未来槽位和服务停机。默认地址为 `127.0.0.1:8094`。浏览器实测记录见下方 10 月 28 日条目。

初次验证日期为 2026-10-28。

## 基线与产物

- KCQ 基线：`d976a96ff60c45c9a2e449d8080fd3b0657caf68`。
- 独立服务：`KCQ-Calendar-Connector`。
- 可执行文件：`bin/calendar-server.exe`。
- 本地数据库：`data/calendar.db`，2015-01-01 至 2026-12-31，五个市场各 4383 条自然日期，共 21915 条。
- 当前实际来源：`exchange_calendars==4.13.2`；A 股显式使用 `--cn-source exchange_calendars`。
- Baostock 采集器保留。本次在独立环境和原有环境均遇到匿名登录网络超时，未将失败响应写入数据库，未声称该轮采集成功。

## 已通过的检查

- `go test ./...`：领域逻辑、HTTP 和真实 SQLite 测试。
- `go test -race ./...`：包含更新事务与读取事务同时执行的快照一致性测试。
- `go vet ./...`。
- `python -m unittest discover -s scripts/tests -v`：七项通过，覆盖缺失记录、周末全休市、显式来源选择、人工修订、未知版本和事务失败回滚。
- `python scripts/verify_local.py`：启动编译后的真实 Go 进程，用数据库副本跑 HTTP 查询，验证五市场、节假日差异、A 股 2026 年 242 个计划交易日、感恩节、夏令时、明确拒绝未覆盖范围 / 期货 / 分钟线、以及不用重启即可读到更新快照。

## 验证边界

- 已验证独立服务及 V1 日线兼容接口，随后完成下述 Baostock 路由接入；未改 KCQ 前端，未执行浏览器 UI 端到端操作。
- 当前覆盖是计划交易日期，不代表所有历史临时休市都已被来源完整收录。
- 不提供品种停牌状态、期货夜盘、分钟槽位或半日市具体交易时间。

## Baostock 接入验证（2026-10-28）

- Baostock-Tradingview-Connector：28 项 unittest 通过，含新增 8 项日历用例；检查可选配置、probe 与沪深品种能力、市场映射、时区覆盖、未支持源/市场/周期、参数错误、覆盖不足、超时、断连与无效响应。单元测试隔离网络，不声称行情登录成功。
- `scripts/verify_baostock.py` 使用真实 Go 进程、生产 Connector app/路由及数据库副本，完成真实 HTTP 转发。仅关闭会联网预热股票目录的 lifespan；不替换日历 HTTP 或数据库实现。
- 沪市锚点 2026-09-30 返回 10-08、10-09、10-12；排除国庆休市和 10-10 调休上班的周六。预期依据[上交所 2026 年中秋国庆休市公告](https://www.sse.com.cn/disclosure/announcement/general/c/c_20260915_10832273.shtml)，并与本地 exchange_calendars 规则样本核对。
- 深市锚点 2026-10-30 返回 11-02、11-03；时刻保持上海当地零点。
- 未支持周期/源、超范围日期和非法计数返回正确 V1 错误；覆盖末尾返回实际可用前缀；终止日历进程后 Connector 返回 502 / UPSTREAM_UNAVAILABLE。
- 使用 KCQ `d976a96f` 实际源码的 HTTP Transport、Provider 和 DataBuffer，验证未来槽位可消费且索引日期与预期一致。源级/品种级能力来自 Connector 生产函数，锚点来自行情生产日期转换器；未执行联网 probe、证券搜索或拉取真实价格，价格字段仅为时间轴测试占位。
- 脚本支持用已有 tsx 运行，未修改 KCQ 源码；临时服务、端口与数据库副本在验证结束后清理。

```powershell
..\Baostock-Tradingview-Connector\.venv\Scripts\python.exe scripts/verify_baostock.py --connector ../Baostock-Tradingview-Connector --kcq ../KLineChartQuant --tsx-cli ../KLineChartQuant/node_modules/tsx/dist/cli.mjs
```

## 浏览器实测补充（2026-10-28，香港时间约 21:11–21:18）

- 启动 KCQ `d976a96f` 的真实 Vue preview、Baostock 生产 app 和 Go 日历服务；没有替换证券搜索或行情响应。为限制本轮等待，在验证启动器中设置 socket 默认超时 8 秒，并关闭启动时自动目录预热，目录仍由真实搜索触发加载。
- 首次 Baostock 登录超时，probe 返回 offline。随后真实 probe 恢复 online，响应包含 `tradingCalendar: true`；浏览器“行情数据源”面板也显示 BaoStock 在线（428ms）。
- 在“选择商品”中选中 BaoStock，搜索 `600000`。Connector 实际执行 `query_stock_basic()` 拉取股票目录，登录成功后数据接收超时，搜索请求约 21.47 秒后返回 502。浏览器显示“搜索失败”。
- 因无法取得真实品种，未能继续加载 K 线，也未能验证浏览器图表自动发起日历请求、显示未来时间轴。当前不能宣称浏览器端到端验收通过；阻塞发生在行情证券目录上游，不是已验证的 Calendar HTTP 查询路径。
- 同轮重跑 Connector 28 项测试、Calendar Go 测试与 vet、Python 7 项测试，以及真实 HTTP + KCQ Provider/DataBuffer 联调，均通过。后者仍是文中所述的隔离验证，不替代真实行情 UI 验收。
