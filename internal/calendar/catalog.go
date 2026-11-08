// 本文件加载唯一的市场配置，供服务解析市场别名和时区。
package calendar

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"
)

//go:embed markets.json
var marketDefinitions []byte

// Catalog 保存不可变的市场定义及别名索引。
type Catalog struct{ markets map[string]Market }

// NewCatalog 加载内置配置并拒绝重复别名和无效时区。
func NewCatalog() (*Catalog, error) {
	var config struct {
		Markets []Market `json:"markets"`
		Sources map[string]struct {
			URL string `json:"url"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(marketDefinitions, &config); err != nil {
		return nil, err
	}
	c := &Catalog{markets: make(map[string]Market)}
	for _, item := range config.Markets {
		item.SourceURL = config.Sources[item.Source].URL
		if _, err := time.LoadLocation(item.TimeZone); err != nil {
			return nil, err
		}
		for _, alias := range append([]string{item.Exchange}, item.Aliases...) {
			if _, exists := c.markets[alias]; exists {
				return nil, fmt.Errorf("duplicate market alias %s", alias)
			}
			c.markets[alias] = item
		}
	}
	return c, nil
}

// Resolve 将交易所代码或别名映射为规范市场，未知值明确报错。
func (c *Catalog) Resolve(value string) (Market, error) {
	market, ok := c.markets[strings.ToUpper(strings.TrimSpace(value))]
	if !ok {
		return Market{}, Unsupported("unsupported exchange %q", value)
	}
	return market, nil
}
