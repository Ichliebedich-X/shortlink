// Package handler 负责 HTTP 参数解析、响应写入和错误码映射。
package handler

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"shortlink/internal/service"
	"shortlink/internal/stats"
	"shortlink/response"
	"time"

	"github.com/gin-gonic/gin"
)

// LinkServiceAPI 是 handler 需要的业务能力。
// 用接口而不是具体类型，测试时可以注入假实现，不用连数据库。
type LinkServiceAPI interface {
	CreateLink(ctx context.Context, origin string, expireAt *time.Time) (string, error)
	Resolve(ctx context.Context, code string) (string, error)
}

// EventRecorder 是访问事件记录能力。
type EventRecorder interface {
	Record(stats.Event)
}

type LinkHandler struct {
	svc       LinkServiceAPI
	collector EventRecorder
}

// 接收请求的结构体：专门用来接收前端传的 JSON，加上 binding tag 做形状校验
type createLinkReq struct {
	Origin   string `json:"origin" binding:"required,max=2048"`
	ExpireAt string `json:"expire_at"` // 新增字段，类型是 string，因为前端传过来就是字符串
}

// 返回成功的结构体：不用 gin.H，用结构体锁定字段类型
type createLinkResp struct {
	Code     string `json:"code"`
	ShortURL string `json:"short_url"`
}

// NewLinkHandler 把业务接口和统计回调注入 handler。
func NewLinkHandler(svc LinkServiceAPI, collector EventRecorder) *LinkHandler {
	return &LinkHandler{
		svc:       svc,
		collector: collector,
	}
}

// ==============================
// 1. 创建短链接口
// ==============================

// POST /api/v1/links
func (h *LinkHandler) CreateLink(c *gin.Context) {
	// 1. 解析参数，形状校验
	var req createLinkReq
	if err := c.ShouldBindJSON(&req); err != nil {
		// 参数错误
		response.Fail(c, http.StatusBadRequest, response.CodeBadRequest, "参数错误，必须包含 origin 字段")
		return
	}

	// 解析 expire_at
	var expireAt *time.Time
	if req.ExpireAt != "" {
		// RFC3339 是一种国际标准的时间格式，比如 2026-11-01T22:00:00+08:00
		t, err := time.Parse(time.RFC3339, req.ExpireAt)
		if err != nil {
			response.Fail(c, http.StatusBadRequest, response.CodeBadRequest, "expire_at 格式必须是 RFC3339，例如 2026-11-01T22:00:00+08:00")
			return
		}
		expireAt = &t // 取地址塞进指针
	}

	// 2. 调 Service
	// 注意：传的是 c.Request.Context()，而不是把 gin.Context 传下去
	code, err := h.svc.CreateLink(c.Request.Context(), req.Origin, expireAt)
	if err != nil {
		if errors.Is(err, service.ErrInvalidOrigin) || errors.Is(err, service.ErrInvalidExpireAt) {
			// 业务规则错误，返回 400
			response.Fail(c, http.StatusBadRequest, response.CodeBadRequest, err.Error())
			return
		}
		// 数据库或 Redis 挂了，返回 500
		// 打一条日志：记录传入的 origin 和具体的报错
		slog.Error("CreateLink failed", "request_id", c.GetString("request_id"), "err", err)
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "服务器内部错误")
		return
	}

	// 3. 成功，返回结果
	response.OK(c, createLinkResp{
		Code:     code,
		ShortURL: "http://localhost:8080/" + code,
	})
}

// ==============================
// 2. 跳转接口
// ==============================

// GET /:code
func (h *LinkHandler) Redirect(c *gin.Context) {
	// 1. 获取参数
	code := c.Param("code")

	// 2. 呼叫 Service
	origin, err := h.svc.Resolve(c.Request.Context(), code)

	// 3. 错误分类处理
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			response.Fail(c, http.StatusNotFound, response.CodeNotFound, "短链不存在")
		} else if errors.Is(err, service.ErrExpired) {
			response.Fail(c, http.StatusGone, response.CodeExpired, "短链已过期")
		} else {
			log.Printf("Redirect 失败: code=%s err=%v", code, err)
			response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "服务器内部错误")
		}
		return
	}

	// 放到 302 之前，把事件丢进流水线
	// 非阻塞：如果队列满了直接丢，绝不卡住用户跳转
	h.collector.Record(stats.Event{
		Code: code,
		IP:   c.ClientIP(),
		Time: time.Now(),
	})

	// 4. 核心：302 重定向
	c.Redirect(http.StatusFound, origin)
}
