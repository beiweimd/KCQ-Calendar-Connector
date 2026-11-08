# 日历服务设计决策

## 范围和边界

依据 KCQ 2026-10-28 主线 `d976a96f` 的独立 Connector 组织方式和 V1 日历形状开发。本服务放在 KCQ 同级目录，既不合并到前端 Core，也不混入 GoTDX 的通达信连接管理。

本次约定范围是 A 股、港股、美股的市场级交易日期。只返回明确覆盖的日期；不承诺全品种的实际交易状态。期货和分钟时段需要独立的数据来源及规则，不以不准确的能力声明提前开放。

## 数据和查询分离

- Baostock 与 exchange_calendars 只在离线 Python 工具中运行，Go 请求路径没有网络依赖。
- 市场配置和表结构各有一份事实来源，Go 与 Python 共享文件。
- SQLite 保存每个自然日期的开闭市标记，避免区分不出“休市”和“数据缺失”。
- 多市场更新事务化；读取元数据和记录也使用同一个事务，避免新版本元数据与旧日期混读。
- 支持外部可更新数据库；不将其 embed 到二进制，不缓存整年时间戳。
- 来源版本、覆盖区间、内容版本和人工调整原因随查询暴露，便于追溯。

## 接入 KCQ

V1 路由接受原行情 Connector 的 sourceId，但只提供日历能力。不伪造 probe 的 bars 能力，也不注册不提供 K 线的行情 Provider。Baostock-Tradingview-Connector 已增加可选日历转发，行情 bars 实现保持原样。

日线 timestamp 的时区由调用 Connector 对齐。默认市场时区；UTC 日期标签须显式传 calendarTimeZone。不能用固定 24 小时累加跨越夏令时，也不能自动从模糊 US 标识猜测具体交易所。

Baostock 转发必须设置 `KCQ_CALENDAR_URL` 才声明源级日历能力，仅 SH/SZ 品种声明品种级能力。转发层保留 sourceId、symbol 与 id，把 SH/SZ 映射为 SSE/SZSE，并强制采用与行情 `_timestamp_ms` 一致的 Asia/Shanghai 日期标签。不能让 providerRef 把沪深行情改映射到美股日期。当前仅接受 daily/original；未覆盖日期和未支持周期保留能力错误，网络异常不回退或补造日期。

## exchange_calendars 参考边界

项目已将 exchange_calendars 用于离线规则日历生成；Go 查询路径继续只读 SQLite。上游的 schedule 含 open、break_start、break_end、close，另有 session_minutes 与 trading_index，可用于后续时段模型设计。扩展分钟能力前需要存储这些时段边界并对齐行情源的 K 线标记方式，不能直接把市场级开市日期当作品种的分钟槽位或停牌状态。

参考：[exchange_calendars README](https://github.com/gerrymanoim/exchange_calendars)。
