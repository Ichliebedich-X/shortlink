// Package stats 异步收集访问事件并批量写入统计表。
package stats

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"shortlink/internal/dao"
)

// Event 表示一次成功跳转产生的访问事件。
type Event struct {
	Code string
	IP   string
	Time time.Time
}

// Collector 用带缓冲的 channel 异步收集事件，并由单个 worker 批量落库。
type Collector struct {
	ch      chan Event
	dao     *dao.StatsDao
	dropped atomic.Uint64
	done    chan struct{}
}

// NewCollector 创建收集器；buffer 是事件队列容量。
func NewCollector(d *dao.StatsDao, buffer int) *Collector {
	return &Collector{
		ch:   make(chan Event, buffer),
		dao:  d,
		done: make(chan struct{}),
	}
}

// Record 非阻塞写入事件；队列满时丢弃并计数，避免阻塞跳转。
func (c *Collector) Record(e Event) {
	select {
	case c.ch <- e:
	default:
		c.dropped.Add(1)
	}
}

// Run 在后台消费事件，按 2 秒或 100 条聚合后批量写入统计表。
func (c *Collector) Run(ctx context.Context) {
	defer close(c.done)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// key 是 "日期|短码"，value 是这段时间内的 PV。
	batch := make(map[string]int64)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		for key, pv := range batch {
			parts := strings.Split(key, "|")
			date, code := parts[0], parts[1]

			// 使用 Background 而不是请求 ctx：请求已经结束，统计数据仍要写完。
			if err := c.dao.IncrPV(context.Background(), code, date, pv); err != nil {
				slog.Error("stats flush failed", "code", code, "date", date, "err", err)
			}
		}
		batch = make(map[string]int64)
	}

	for {
		select {
		case e, ok := <-c.ch:
			if !ok {
				flush()
				return
			}

			key := e.Time.Format("2006-01-02") + "|" + e.Code
			batch[key]++

			if len(batch) >= 100 {
				flush()
			}

		case <-ticker.C:
			flush()
			if n := c.dropped.Swap(0); n > 0 {
				slog.Warn("stats dropped", "count", n)
			}

		case <-ctx.Done():
			flush()
			return
		}
	}
}

// Wait 阻塞到 Run 退出，供优雅关闭时等待缓冲区写完。
func (c *Collector) Wait() {
	<-c.done
}
