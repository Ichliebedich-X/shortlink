// Package config 读取服务运行所需的环境变量。
package config

import "os"

type Config struct {
	MySQLDSN  string
	RedisAddr string
}

// Load 返回当前环境的配置；未设置的环境变量使用本地开发默认值。
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
