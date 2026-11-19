package service

import (
	"net/url"
	"time"
)

// maxOriginLength 和数据库里 origin VARCHAR(2048) 对齐。
const maxOriginLength = 2048

// ValidateOrigin 校验原始链接是否是合法的 http/https 地址。
//
// 单独抽成函数的目的：业务校验可以在没有数据库、没有 Redis 的情况下做单元测试。
// CreateLink 只负责"调用校验 + 走流程"，不再把规则写在主流程里。
func ValidateOrigin(origin string) error {
	// 第 1 步：空值和长度。
	if origin == "" || len(origin) > maxOriginLength {
		return ErrInvalidOrigin
	}

	// 第 2 步：必须是合法的 URI。
	u, err := url.ParseRequestURI(origin)
	if err != nil {
		return ErrInvalidOrigin
	}

	// 第 3 步：必须有 host，scheme 只能是 http/https。
	// 这一步挡住 "javascript:alert(1)"、"ftp://..." 和 "http://"。
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ErrInvalidOrigin
	}

	return nil
}

// ValidateExpireAt 校验过期时间。
// nil 表示永不过期；非 nil 必须晚于当前时间。
func ValidateExpireAt(expireAt *time.Time) error {
	if expireAt != nil && !expireAt.After(time.Now()) {
		return ErrInvalidExpireAt
	}
	return nil
}
