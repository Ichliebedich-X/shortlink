// Package base62 提供短码所需的 62 进制编码和解码。
package base62

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

const charset = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// Encode把十进制数转成 base62字符串
func Encode(n uint64) string {
	//0直接返回0，否则下面循环进不去会返回空字符串
	if n == 0 {
		return "0"
	}

	var result []byte
	for n > 0 {
		//base62:取余数，找到对应的字符
		remainder := n % 62
		result = append(result, charset[remainder])
		//除以62继续下一位
		n /= 62
	}

	//从下到上反转切片
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return string(result)
}

// Decode 把 base62 字符串还原成数字。
// 启动校准发号器时会用它从已有短码中找出最大编号。
func Decode(s string) (uint64, error) {
	if s == "" {
		return 0, errors.New("empty base62 string")
	}

	var n uint64
	for _, ch := range s {
		idx := strings.IndexRune(charset, ch)
		if idx < 0 {
			return 0, fmt.Errorf("invalid base62 char %q", ch)
		}
		if n > (math.MaxUint64-uint64(idx))/62 {
			return 0, errors.New("base62 value overflow")
		}
		n = n*62 + uint64(idx)
	}
	return n, nil
}
