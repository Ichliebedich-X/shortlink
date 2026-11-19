package bloom

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestFalsePositiveRate 实测布隆过滤器的误判率。
//
// 默认跳过：它需要真实 Redis，并且会写入约 70 万个位操作。
// 手动运行：
//
//	$env:BLOOM_FPR = "1"
//	go test ./pkg/bloom -run TestFalsePositiveRate -v
func TestFalsePositiveRate(t *testing.T) {
	if os.Getenv("BLOOM_FPR") != "1" {
		t.Skip("set BLOOM_FPR=1 to run (requires Redis)")
	}

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}

	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis not available at %s: %v", addr, err)
	}

	key := fmt.Sprintf("bloom:fpr:test:%d", time.Now().UnixNano())
	defer rdb.Del(ctx, key) // 测完清掉，不污染正式 key

	// 用缩小版参数快速达到设计容量：
	// m = 2^20 位（128 KB），n = 10 万，m/n ≈ 10，和生产参数同比例。
	const (
		bits    = 1 << 20
		items   = 100_000
		samples = 10_000
		k       = 7
	)

	f := &Filter{rdb: rdb, key: key, bits: bits, k: k}

	// 第 1 步：写入 10 万个"存在的"短码。
	codes := make([]string, items)
	for i := range codes {
		codes[i] = fmt.Sprintf("present-%d", i)
	}
	if err := f.Rebuild(ctx, codes); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	// 第 2 步：查询 1 万个"肯定不存在"的随机码，
	// 统计有多少被误判成"可能存在"。
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	falsePositives := 0
	for i := 0; i < samples; i++ {
		code := fmt.Sprintf("absent-%016x", r.Uint64())
		ok, err := f.Exists(ctx, code)
		if err != nil {
			t.Fatalf("exists: %v", err)
		}
		if ok {
			falsePositives++
		}
	}

	rate := float64(falsePositives) / float64(samples) * 100
	t.Logf("bits=%d items=%d k=%d samples=%d false_positives=%d rate=%.2f%%",
		bits, items, k, samples, falsePositives, rate)

	// 理论值约 0.8%，给统计波动留一点余量。
	if rate > 3 {
		t.Fatalf("false positive rate %.2f%% is too high", rate)
	}
}
