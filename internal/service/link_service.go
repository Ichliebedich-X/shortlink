// Package service 承载短链接的业务规则：校验、发号、缓存读写和回源。
package service

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"shortlink/internal/dao"
	"shortlink/internal/model"
	"shortlink/pkg/base62"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

var (
	ErrInvalidOrigin   = errors.New("无效的原始链接")
	ErrInvalidExpireAt = errors.New("过期时间必须晚于当前时间")
	ErrNotFound        = errors.New("短链不存在")
	ErrExpired         = errors.New("短链已过期")
	ErrCodeConflict    = errors.New("短码生成冲突，请重试")
)

// createLinkMaxRetry 短码冲突时的最大重试次数。
// 两三次足够覆盖"Redis 计数刚回退几个号"的情况。
const createLinkMaxRetry = 3

// 定义时间
const (
	defaultTTL   = 24 * time.Hour   //正常缓存24h
	nullTTL      = 60 * time.Second //空值缓存60s
	nullValue    = ""               //空字符串代表不存在
	ttlJitterMax = 5 * time.Minute  // 解决雪崩：最大抖动 5 分钟
)

type LinkService struct {
	linkDao LinkRepo
	cache   Cache
	group   singleflight.Group // 防击穿：按 key 合并同进程内的并发请求
	bloom   BloomFilter        // 防穿透：布隆过滤器
}

// NewLinkService 组装 Service。依赖以接口形式传入，方便单测替换。
func NewLinkService(linkDao LinkRepo, cache Cache, bloomFilter BloomFilter) *LinkService {
	return &LinkService{
		linkDao: linkDao,
		cache:   cache,
		bloom:   bloomFilter,
	}
}

// CreateLink 创建短链：校验 → 取号 → 编码 → 落库（冲突则重试）。
func (s *LinkService) CreateLink(ctx context.Context, origin string, expireAt *time.Time) (string, error) {
	// 第 1 步：业务校验 —— 必须是合法的 http/https 链接。
	if err := ValidateOrigin(origin); err != nil {
		return "", err
	}

	// 第 2 步：业务校验 —— 过期时间必须晚于当前时间。
	if err := ValidateExpireAt(expireAt); err != nil {
		return "", err
	}

	// 第 3 步：取号、编码、落库；冲突就换一个号重试。
	//
	// 为什么会有冲突：短码来自 Redis 计数器，如果 Redis 数据被清空、
	// 计数回退到比数据库更小的值，算出来的 code 就可能撞上已有记录。
	// 数据库的 uk_code 唯一索引会拦住这次插入，我们识别出来就重试，
	// 而不是把 500 直接甩给用户。
	for attempt := 1; attempt <= createLinkMaxRetry; attempt++ {
		// 3.1 从 Redis 取一个全局自增号。
		// INCR 是原子操作，多实例部署时也不会同时拿到同一个数字。
		id, err := s.cache.Incr(ctx, "shortlink:id")
		if err != nil {
			return "", err // Redis 真挂了，交给 handler 返回 500
		}

		// 3.2 数字转 62 进制。6 位可以表示约 568 亿条。
		code := base62.Encode(uint64(id))

		// 3.3 组装数据模型。ExpireAt 是 nil 时表示永不过期。
		link := &model.TLink{
			Code:     code,
			Origin:   origin,
			ExpireAt: expireAt,
			GID:      "default",
			Status:   1,
		}

		// 3.4 写库。唯一索引是并发下的最后一道防线。
		if err := s.linkDao.Create(ctx, link); err != nil {
			if dao.IsDuplicateKey(err) {
				slog.Warn("short code conflict, retrying",
					"code", code,
					"attempt", attempt,
				)
				continue // 换下一个号再试
			}
			return "", err // 其他数据库错误（连接断开、字段超长等）直接上抛
		}

		// 3.5 落库成功后，必须立刻把短码加入布隆过滤器。
		//
		// 为什么要在落库之后？
		// 如果先加过滤器，万一 DB 插入失败（比如连接断开），过滤器里就会多出一个
		// 实际不存在的短码。这违背了布隆过滤器“绝不产生假阴性”的定律。
		//
		// 如果这里报错（Add 失败）会怎样？
		// 会导致“假阴性”：数据库里确实有，但过滤器说没有。
		// 之后用户访问这个短码会被直接拒绝（404）。所以必须用 slog.Error 报警，
		// 但这属于极少见的 Redis 故障，不能因为过滤器的故障影响用户创建短链。
		if s.bloom != nil {
			if err := s.bloom.Add(ctx, code); err != nil {
				slog.Error("bloom add failed", "code", code, "err", err)
			}
		}

		// 3.6 成功。Create 会把 MySQL 生成的 id、created_at 回填到 link 上。
		return code, nil
	}

	// 重试用完还冲突，说明发号器落后太多，需要校准（生产上用号段或持久化计数）。
	return "", ErrCodeConflict
}

// errCacheMiss 是内部信号：缓存里没有这个 key，需要回源。
// 它不是业务错误，只用来区分"没命中"和"命中了一个空值标记"。
var errCacheMiss = errors.New("cache miss")

// Resolve 接收短码，返回原始长链接。
//
// 读链路分四步：
//  1. 先查 Redis（挡重复）；
//  2. 未命中时问布隆过滤器（挡乱码穿透）；
//  3. 用 singleflight 合并同一短码的并发请求（防击穿）；
//  4. 回源 MySQL、校验状态、算 TTL 并回填缓存。
func (s *LinkService) Resolve(ctx context.Context, code string) (string, error) {
	key := "link:" + code // 加前缀，防止和其他业务的 key 冲突

	// 第 1 步：先看缓存。命中直接返回，连 singleflight 和布隆过滤器都不用进。
	// 缓存是热路径，速度最快，必须放在第一关。
	if origin, err := s.readCache(ctx, key, code); err == nil {
		return origin, nil
	} else if !errors.Is(err, errCacheMiss) {
		// 命中了空值缓存 → 说明之前查库确认过不存在，直接 404。
		return "", err
	}

	// 第 2 步：防海量随机穿透。缓存未命中，立刻问布隆过滤器。
	//
	// 为什么要放在缓存之后？
	// 因为缓存的命中率极高，如果放在前面，每次都能命中的正常请求反而要多查一次 Redis 位图。
	// 放在缓存未命中之后，只有真正可能不存在的请求才会进入这里，省网络往返。
	//
	// 注意这里是“降级处理”：如果 Redis 挂了，Exists 会报错。
	// 此时不能 return 404，因为可能造成全站崩溃。只能记日志，放行去查库。
	if s.bloom != nil {
		exists, err := s.bloom.Exists(ctx, code)
		if err != nil {
			slog.Error("bloom check failed, degrade", "code", code, "err", err)
		} else if !exists {
			// 布隆过滤器说“绝对不存在”，直接拦截，连 MySQL 都不碰。
			slog.Debug("bloom rejected", "code", code)
			return "", ErrNotFound
		}
	}

	// 第 3 步：缓存和布隆都过了，交给 singleflight 合并并发请求。
	// 这是为了防止“热点 key 刚过期，一万个人同时查同一个库”的击穿。
	v, err, _ := s.group.Do(key, func() (any, error) {
		// double check：从"发现未命中"到"真正拿到执行权"之间，
		// 可能已经有别的请求把缓存填好了，再查一次能省一次回源。
		if origin, err := s.readCache(ctx, key, code); err == nil {
			return origin, nil
		} else if !errors.Is(err, errCacheMiss) {
			return "", err
		}

		return s.loadAndFill(ctx, key, code)
	})
	if err != nil {
		return "", err
	}

	// singleflight 的返回值是 any，这里断言回 string。
	return v.(string), nil
}

// readCache 读缓存，三种结果：
// 命中真实链接        → 返回值, nil
// 命中空值标记        → "", ErrNotFound（防穿透）
// 未命中 / Redis 故障 → "", errCacheMiss（继续往下查库）
//
// Redis 真挂了不算业务错误：记日志后降级查库，保证用户还能跳转。
func (s *LinkService) readCache(ctx context.Context, key, code string) (string, error) {
	origin, err := s.cache.Get(ctx, key)
	if err == nil {
		if origin == nullValue {
			slog.Debug("cache hit null (防穿透)", "code", code)
			return "", ErrNotFound
		}
		slog.Debug("cache hit", "code", code)
		return origin, nil
	}

	// redis.Nil 是"这个 key 不存在"的正常信号，不是故障。
	if errors.Is(err, redis.Nil) {
		slog.Debug("cache miss", "code", code)
		return "", errCacheMiss
	}

	// 其他错误才是 Redis 真的出问题了：记日志，降级去查库。
	slog.Error("redis get failed", "code", code, "err", err)
	return "", errCacheMiss
}

// loadAndFill 回源：查库 → 校验状态/过期 → 算 TTL → 回填缓存。
//
// 它只在缓存未命中、并且被 singleflight 选中时执行，
// 所以同一时刻同一个 code 最多只有一个 goroutine 走到这里。
func (s *LinkService) loadAndFill(ctx context.Context, key, code string) (string, error) {
	slog.Debug("load from db", "code", code)

	// 第 1 步：查 MySQL。
	link, err := s.linkDao.GetByCode(ctx, code)
	if err != nil {
		if errors.Is(err, dao.ErrNotFound) {
			// 防穿透：确认不存在，也写一个短 TTL 的空值。
			// 之后同样的请求直接命中空值，不再打到 MySQL。
			if err := s.cache.Set(ctx, key, nullValue, nullTTL); err != nil {
				slog.Error("redis set null failed", "code", code, "err", err)
			}
			return "", ErrNotFound
		}
		return "", err // 其他数据库错误交给 handler 返回 500
	}

	// 第 2 步：禁用/逻辑删除的短链不缓存，避免缓存长期放行。
	if link.DelFlag != 0 || link.Status != 1 {
		return "", ErrNotFound
	}

	// 第 3 步：防雪崩 —— 默认 TTL 加随机抖动，
	// 避免大批 key 在同一秒集体过期。
	ttl := defaultTTL + time.Duration(rand.Int63n(int64(ttlJitterMax)))

	// 但抖动后的 TTL 绝不能超过短链真实的剩余寿命。
	if link.ExpireAt != nil {
		remaining := time.Until(*link.ExpireAt)
		if remaining <= 0 {
			return "", ErrExpired // 已经过期，handler 返回 410
		}
		if remaining < ttl {
			ttl = remaining
		}
	}

	// 第 4 步：回填缓存。失败只记日志，不影响用户跳转：能跳比快重要。
	if err := s.cache.Set(ctx, key, link.Origin, ttl); err != nil {
		slog.Error("redis set failed", "code", code, "err", err)
	}

	return link.Origin, nil
}
