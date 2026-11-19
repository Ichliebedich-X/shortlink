package service

import (
	"strings"
	"testing"
	"time"
)

func TestValidateOrigin(t *testing.T) {
	// 构造一个正好 2048 长度的合法 URL
	prefix := "https://example.com/"
	exact2048 := prefix + strings.Repeat("a", 2048-len(prefix))
	tooLong := prefix + strings.Repeat("a", 2048-len(prefix)+1)

	cases := []struct {
		name   string
		origin string
		want   error
	}{
		{"合法 https", "https://go.dev", nil},
		{"合法带参数", "http://example.com/path?q=1", nil},
		{"正好 2048 长度", exact2048, nil},
		{"空字符串", "", ErrInvalidOrigin},
		{"没有 scheme", "go.dev", ErrInvalidOrigin},
		{"错误的 scheme", "ftp://example.com", ErrInvalidOrigin},
		{"XSS 攻击", "javascript:alert(1)", ErrInvalidOrigin},
		{"没有 host", "http://", ErrInvalidOrigin},
		{"带空格的乱码", "not a url", ErrInvalidOrigin},
		{"超长", tooLong, ErrInvalidOrigin},
	}

	for _, c := range cases {
		c := c // 闭包安全（Go 1.22 前必须写）
		t.Run(c.name, func(t *testing.T) {
			got := ValidateOrigin(c.origin)
			if got != c.want {
				t.Fatalf("ValidateOrigin(%q) = %v, want %v", c.origin, got, c.want)
			}
		})
	}
}

func TestValidateExpireAt(t *testing.T) {
	now := time.Now()
	oneMinLater := now.Add(time.Minute)
	oneMinAgo := now.Add(-time.Minute)

	cases := []struct {
		name     string
		expireAt *time.Time
		want     error
	}{
		{"nil 永不过期", nil, nil},
		{"一分钟后", &oneMinLater, nil},
		{"一分钟前", &oneMinAgo, ErrInvalidExpireAt},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := ValidateExpireAt(c.expireAt)
			if got != c.want {
				t.Fatalf("ValidateExpireAt() = %v, want %v", got, c.want)
			}
		})
	}
}
