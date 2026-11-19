// Package cache 把 go-redis 封装成 service 需要的缓存接口。
package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis 把 go-redis 的命令结果转换成 service.Cache 需要的普通返回值。
type Redis struct {
	client *redis.Client
}

// NewRedis 创建 Redis 缓存适配器。
func NewRedis(client *redis.Client) *Redis {
	return &Redis{client: client}
}

func (r *Redis) Get(ctx context.Context, key string) (string, error) {
	return r.client.Get(ctx, key).Result()
}

func (r *Redis) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}

func (r *Redis) Incr(ctx context.Context, key string) (int64, error) {
	return r.client.Incr(ctx, key).Result()
}
