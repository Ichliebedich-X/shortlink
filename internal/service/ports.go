package service

import (
	"context"
	"time"

	"shortlink/internal/model"
)

// LinkRepo 是 service 需要的短链存储能力。
// 用接口而不是具体 DAO，测试时可以注入假实现。
type LinkRepo interface {
	Create(ctx context.Context, link *model.TLink) error
	GetByCode(ctx context.Context, code string) (*model.TLink, error)
}

// Cache 是 service 需要的缓存能力。
// 用接口隔离 Redis 命令，单测不需要连真实 Redis。
type Cache interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Incr(ctx context.Context, key string) (int64, error)
}

// BloomFilter 是防穿透过滤器需要的能力。
type BloomFilter interface {
	Add(ctx context.Context, code string) error
	Exists(ctx context.Context, code string) (bool, error)
}
