// Package businessclock 提供业务日历值使用的北京时间（UTC+8）时钟。
//
// 本包只用于生成“业务日历值”，例如单号中的日期段、按自然日计算的业务判断。
// 存储时间戳（CreatedAt/UpdatedAt、审计 OccurredAt、各类过期时间等）必须继续使用
// time.Now().UTC()，不得改用本包，否则数据库中的时间语义会从 UTC 漂移到北京时间。
package businessclock

import (
	"sync"
	"time"
)

// dateLayout 是业务单号日期段使用的日历格式，例如 20260101。
const dateLayout = "20060102"

// 时区固定为北京时间：优先使用 IANA 时区数据，缺失 tzdata 的环境回落固定 +08:00。
// 用 sync.OnceValue 缓存，避免每次取业务日历值都解析时区。
var beijing = sync.OnceValue(func() *time.Location {
	if location, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return location
	}
	return time.FixedZone("CST", 8*60*60)
})

// Location 返回业务日历使用的北京时间时区。
func Location() *time.Location {
	return beijing()
}

// Now 返回当前时刻在北京时间下的表示。它只用于业务日历值，存储时间戳仍取 time.Now().UTC()。
func Now() time.Time {
	return time.Now().In(Location())
}

// DateString 返回 t 在北京日历下的 20060102 形式日期段。
// 传入 UTC 时间戳时同样先换算到北京时间，因此北京时间 00:00–08:00 会归属当天而不是 UTC 的前一天。
func DateString(t time.Time) string {
	return t.In(Location()).Format(dateLayout)
}
