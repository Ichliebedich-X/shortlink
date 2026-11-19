// Package response 定义统一响应体和业务错误码。
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 业务错误码：HTTP 状态码给网关/浏览器/监控看，业务码给前端做逻辑分支。
const (
	CodeOK         = 0
	CodeBadRequest = 40001
	CodeNotFound   = 40401
	CodeExpired    = 41001
	CodeInternal   = 50000
)

// Body 是全局统一的响应体结构
type Body struct {
	Code int    `json:"code"` // 业务码
	Msg  string `json:"msg"`  // 提示信息
	Data any    `json:"data"` // 真正的数据，any 代表可以是任何类型
}

// OK 成功时的统一返回
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Body{
		Code: CodeOK,
		Msg:  "ok",
		Data: data,
	})
}

// Fail 失败时的统一返回
func Fail(c *gin.Context, httpStatus, bizCode int, msg string) {
	c.JSON(httpStatus, Body{
		Code: bizCode,
		Msg:  msg,
		Data: nil, // 失败时 data 永远是 nil
	})
}
