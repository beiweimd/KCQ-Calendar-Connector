// 用 KCQ 的实际 Provider、HTTP Transport 和图表 DataBuffer 消费本地服务返回的日历。
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'

const fixture = JSON.parse(readFileSync(process.argv[2], 'utf8'))
// 目标代码通过参数选择；不拷贝或改写 KCQ 实现。
const moduleUrl = (relative: string) => pathToFileURL(join(fixture.kcqRoot, relative)).href
const { createHttpMarketDataTransport } = await import(moduleUrl('packages/core/src/data/provider/protocol/impl/httpTransport.ts'))
const { createMarketDataProvider } = await import(moduleUrl('packages/core/src/data/provider/protocol/impl/provider.ts'))
const { DataBuffer } = await import(moduleUrl('packages/core/src/data/buffer/impl/dataBuffer.ts'))
const provider = createMarketDataProvider({
  source: { id: 'baostock', displayName: 'BaoStock', capabilities: fixture.capabilities },
  transport: createHttpMarketDataTransport({ baseUrl: fixture.baseUrl }),
})

for (const { request, expected } of fixture.cases) {
  const calendar = await provider.tradingCalendar.fetch(request)
  assert.deepEqual(calendar.futureTimestamps, expected)
  const buffer = new DataBuffer()
  // 只使用生产日期转换器生成的锚点；价格占位不参与日历验证，也不声称行情联网成功。
  buffer.mergePage(
    [{ timestamp: request.anchorTimestamp, open: 1, high: 1, low: 1, close: 1, volume: 0 }],
    'exhausted',
    'Asia/Shanghai',
  )
  assert.equal(buffer.setTradingCalendar(calendar), true)
  assert.equal(buffer.coversTradingCalendar(expected.length), true)
  assert.deepEqual(expected.map((_: number, index: number) => buffer.getFutureTimestamp(index + 1)), expected)
  assert.equal(buffer.getFutureTimestamp(expected.length + 1), null)
  buffer.dispose()
}
await assert.rejects(
  provider.tradingCalendar.fetch({ ...fixture.cases[0].request, period: '5min' }),
  (error: { code: string }) => error.code === 'UNSUPPORTED_CAPABILITY',
)
console.log('KCQ Provider -> Connector -> Calendar -> DataBuffer: passed')
