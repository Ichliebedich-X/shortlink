// Package middleware 提供请求级日志和 request_id。
package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RequestLogger 是一个 Gin 中间件
func RequestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now() // 1. 进门看表，记录开始时间

		// 2. 处理 Request ID
		reqID := c.GetHeader("X-Request-ID") // 看看前端有没有带过来
		if reqID == "" {
			reqID = uuid.NewString() // 没带就现场生成一个 UUID
		}
		c.Set("request_id", reqID)      // 存到上下文里，后面的 Handler 可以拿
		c.Header("X-Request-ID", reqID) // 塞进响应头，返回给前端，方便前端报错时提供这个ID

		// 3. 放行让后面的 Handler 去处理业务
		c.Next()

		// 4. 处理完回来，再看表，算耗时
		cost := time.Since(start)

		// 5. 提取响应状态码（重点：必须在 c.Next() 之后拿，之前拿永远是 200）
		status := c.Writer.Status()

		// 6. 组装日志字段
		attrs := []any{
			"request_id", reqID,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"cost_ms", cost.Milliseconds(),
			"ip", c.ClientIP(),
		}

		// 7. 如果耗时超过 1 秒，升级为 Warn 级别（慢请求预警），否则打 Info 级别
		if cost > time.Second {
			logger.Warn("slow request", attrs...)
			return
		}
		logger.Info("request", attrs...)
	}
}
