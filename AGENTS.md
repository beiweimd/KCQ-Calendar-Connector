# KCQ Calendar Connector 开发约定

- 独立 Go module，HTTP 服务仅查询本地 SQLite；Python 负责采集与事务更新。
- 接口契约与业务类型放 `internal/calendar`，SQLite 放 `internal/storage`，HTTP 放 `internal/httpapi`，启动装配放 `cmd/server`。
- 市场、别名、时区与来源只在 `internal/calendar/markets.json` 定义，Go 和 Python 共用。
- 不支持的市场、周期和覆盖范围必须明确报错，禁止猜测交易日或回退其他来源。
- 文件、函数及关键规则使用简洁中文注释；测试说明实际验证场景。
- 日期统一为交易所当地的 `YYYY-MM-DD`；时间戳使用 UTC 毫秒。
- 测试使用真实 SQLite 和明确的历史样本；网络采集与离线测试分开。
- 验证：`go test ./...`、`go vet ./...`、`uv run python -m unittest discover -s scripts/tests`。
- 未明确要求时不提交、不推送、不创建 PR；采集数据库属于本地生成物。
