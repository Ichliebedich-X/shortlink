// Package bloom 用 Redis 位图实现一个布隆过滤器。
//
// 它解决的问题：大量"根本不存在"的短码请求会穿过缓存、直接打到 MySQL。
// 布隆过滤器可以在查库之前判断：
//   - 一定不存在 → 直接 404
//   - 可能存在   → 继续查缓存/数据库（约 1% 误判）
//
// 为什么用 Redis 位图，而不是内存里的布隆过滤器：
//  1. 多个实例需要看到同一份数据；
//  2. 原生 Redis 就支持 SETBIT/GETBIT，不需要 RedisBloom 模块。
//
// 参数：m = 2^24 位（2 MB），k = 7。
// 按 m/n = 10、k = 7 估算，约能放 160 万个短码，误判率 1% 以内。
package bloom

import (
	"context"
	"hash/fnv"

	"github.com/redis/go-redis/v9"
)

const (
	defaultBits = 1 << 24 // 位数组长度：16,777,216 位 = 2 MB
	defaultK    = 7       // 每个短码设置/检查 7 个位
	batchSize   = 1000    // 重建时每批处理多少个短码
)

type Filter struct {
	rdb  *redis.Client
	key  string
	bits uint64
	k    int
}

// New 创建一个过滤器。key 是 Redis 里位图的键名，
// 所有实例用同一个 key 就能共享同一份过滤结果。
func New(rdb *redis.Client, key string) *Filter {
	return &Filter{rdb: rdb, key: key, bits: defaultBits, k: defaultK}
}

// positions 计算一个短码对应的 k 个位下标。
//
// 第 1 步：用短码本身算哈希 h1。
// 第 2 步：用加盐后的短码算哈希 h2（换一个输入，得到近似独立的第二个哈希）。
// 第 3 步：第 i 个位置 = (h1 + i*h2) % bits。
//
// 这叫双重哈希：只算两次哈希，就能得到 k 个近似独立的位，
// 比真的调用 k 次不同哈希函数更省 CPU。
func (f *Filter) positions(code string) []uint64 {
	// 第 1 步
	h1 := fnv.New64a()
	_, _ = h1.Write([]byte(code))

	// 第 2 步
	h2 := fnv.New64a()
	_, _ = h2.Write([]byte("salt:" + code))

	a := h1.Sum64()
	b := h2.Sum64() | 1 // 保证步长是奇数，减少和 bits 产生公因子的概率

	// 第 3 步
	pos := make([]uint64, f.k)
	for i := 0; i < f.k; i++ {
		pos[i] = (a + uint64(i)*b) % f.bits
	}
	return pos
}

// addScript 原子地把 k 个位设成 1。
// ARGV[1..k] 是 positions 算出来的下标，KEYS[1] 是位图 key。
var addScript = redis.NewScript(`
for i = 1, #ARGV do
    redis.call('SETBIT', KEYS[1], ARGV[i], 1)
end
return 1
`)

// existsScript 原子地检查 k 个位。
// 只要有一位是 0，就返回 0（一定不存在）；全部是 1 才返回 1（可能存在）。
var existsScript = redis.NewScript(`
for i = 1, #ARGV do
    if redis.call('GETBIT', KEYS[1], ARGV[i]) == 0 then
        return 0
    end
end
return 1
`)

// Add 把一个短码写进过滤器。
//
// 步骤：
//  1. 算出 k 个位下标；
//  2. 转成 Lua 参数；
//  3. 用 Lua 一次设置完，保证要么全设置、要么全不设置。
//
// 注意：布隆过滤器只能加、不能删。删除短链时不用碰这里，
// 最坏情况只是这个码以后还会被判为"可能存在"，多查一次库。
func (f *Filter) Add(ctx context.Context, code string) error {
	// 第 1 步
	pos := f.positions(code)

	// 第 2 步
	args := make([]interface{}, len(pos))
	for i, p := range pos {
		args[i] = p
	}

	// 第 3 步
	return addScript.Run(ctx, f.rdb, []string{f.key}, args...).Err()
}

// Exists 判断短码"一定不存在"还是"可能存在"。
//
// 返回值：
//   - false：一定不存在，调用方可以直接返回 404，不用查库；
//   - true ：可能存在（约 1% 误判），需要继续查缓存/数据库。
//
// 步骤：
//  1. 算出 k 个位下标；
//  2. 用 Lua 一次检查完；
//  3. 全部为 1 返回 true，任意一个为 0 返回 false。
func (f *Filter) Exists(ctx context.Context, code string) (bool, error) {
	// 第 1 步
	pos := f.positions(code)
	args := make([]interface{}, len(pos))
	for i, p := range pos {
		args[i] = p
	}

	// 第 2 步
	res, err := existsScript.Run(ctx, f.rdb, []string{f.key}, args...).Int()
	if err != nil {
		return false, err
	}

	// 第 3 步
	return res == 1, nil
}

// Rebuild 用数据库里的全部短码重建过滤器。
//
// 步骤：
//  1. 删掉可能残留的临时 key；
//  2. 没有短码时，清空正式 key 后直接返回；
//  3. 分批把每个短码的 k 个位写进临时 key；
//  4. 用 RENAME 原子替换正式 key。
//
// 为什么写临时 key：
//
//	如果直接 DEL 正式 key 再逐个写，中间有几十毫秒的空窗，
//	这段时间所有查询都会得到"一定不存在"，线上会大面积 404。
//	RENAME 是原子操作，切换那一刻没有空窗。
//
// 为什么用 SetBit 而不是 Lua：
//
//	重建写的是临时 key，没有并发读，不需要原子性；
//	而 pipeline 里的 EVALSHA 在脚本缓存为空时不会自动回退 EVAL。
//	Redis 重启后脚本缓存会清空，用 Lua 重建会直接报 NOSCRIPT，
//	导致服务起不来。
func (f *Filter) Rebuild(ctx context.Context, codes []string) error {
	// 第 1 步：清理上一次失败留下的临时 key
	tmpKey := f.key + ":building"
	if err := f.rdb.Del(ctx, tmpKey).Err(); err != nil {
		return err
	}

	// 第 2 步：空库也要能正常启动。
	// 没有临时 key 时 RENAME 会报 "no such key"，所以提前返回。
	if len(codes) == 0 {
		return f.rdb.Del(ctx, f.key).Err()
	}

	// 第 3 步：分批写入临时 key。
	// 每个短码有 k 个位，一批 1000 个短码 = 7000 条 SETBIT，
	// 不会把单次 pipeline 撑得太大。
	for start := 0; start < len(codes); start += batchSize {
		end := min(start+batchSize, len(codes))

		pipe := f.rdb.Pipeline()
		for _, code := range codes[start:end] {
			for _, p := range f.positions(code) {
				pipe.SetBit(ctx, tmpKey, int64(p), 1)
			}
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
	}

	// 第 4 步：原子切换
	return f.rdb.Rename(ctx, tmpKey, f.key).Err()
}
