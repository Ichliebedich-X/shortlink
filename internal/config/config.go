// Package config 读取服务运行所需的环境变量。
package config

import "os"

type Config struct {
	MySQLDSN  string
	RedisAddr string
}

// Load 返回当前环境的配置。
//
// 本地开发默认连接宿主机上的 MySQL 和 Redis：
// MySQL 127.0.0.1:3306，Redis 127.0.0.1:6379。
// compose 内的 app 会通过环境变量改用 mysql:3306 / redis:6379。
func Load() *Config {
	return &Config{
		MySQLDSN:  getEnv("MYSQL_DSN", "root:root@tcp(127.0.0.1:3306)/shortlink?charset=utf8mb4&parseTime=True&loc=Local"),
		RedisAddr: getEnv("REDIS_ADDR", "127.0.0.1:6379"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
