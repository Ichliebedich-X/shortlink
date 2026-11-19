package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shortlink/internal/service"
	"shortlink/internal/stats"

	"github.com/gin-gonic/gin"
)

// ==== 假对象：假 Service ====
type fakeService struct {
	createCode   string
	createErr    error
	resolveURL   string
	resolveErr   error
	createCalled bool // 用来断言有没有被调用
	lastCode     string
}

func (f *fakeService) CreateLink(ctx context.Context, origin string, expireAt *time.Time) (string, error) {
	f.createCalled = true
	return f.createCode, f.createErr
}

func (f *fakeService) Resolve(ctx context.Context, code string) (string, error) {
	f.lastCode = code
	return f.resolveURL, f.resolveErr
}

// ==== 假对象：假 Recorder ====
type fakeRecorder struct {
	count int
}

func (f *fakeRecorder) Record(e stats.Event) {
	f.count++
}

// ==== 辅助函数：建路由 ====
func newTestRouter(svc LinkServiceAPI, rec EventRecorder) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewLinkHandler(svc, rec)
	r.POST("/api/v1/links", h.CreateLink)
	r.GET("/:code", h.Redirect)
	return r
}

// ==== 测试 POST /api/v1/links ====
func TestCreateLink(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		fakeCode      string
		fakeErr       error
		wantStatus    int
		wantBizCode   int
		wantSvcCalled bool
	}{
		{"成功", `{"origin":"https://go.dev"}`, "abc", nil, 200, 0, true},
		{"JSON 格式坏", `{"origin":`, "", nil, 400, 40001, false},
		{"非法 origin", `{"origin":"not-a-url"}`, "", service.ErrInvalidOrigin, 400, 40001, true},
		{"时间格式错", `{"origin":"https://go.dev", "expire_at":"bad-time"}`, "", nil, 400, 40001, false},
		{"内部错误", `{"origin":"https://go.dev"}`, "", errors.New("db down"), 500, 50000, true},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			svc := &fakeService{createCode: c.fakeCode, createErr: c.fakeErr}
			router := newTestRouter(svc, &fakeRecorder{})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/links", strings.NewReader(c.body))
			req.Host = "localhost:8081"
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			if w.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, c.wantStatus)
			}

			var resp struct {
				Code int `json:"code"`
				Data struct {
					Code     string `json:"code"`
					ShortURL string `json:"short_url"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode body %q: %v", w.Body.String(), err)
			}
			if resp.Code != c.wantBizCode {
				t.Fatalf("biz code = %d, want %d", resp.Code, c.wantBizCode)
			}
			if c.wantStatus == http.StatusOK {
				if resp.Data.Code != c.fakeCode {
					t.Fatalf("data.code = %q, want %q", resp.Data.Code, c.fakeCode)
				}
				wantURL := "http://localhost:8081/" + c.fakeCode
				if resp.Data.ShortURL != wantURL {
					t.Fatalf("data.short_url = %q, want %q", resp.Data.ShortURL, wantURL)
				}
			}

			if svc.createCalled != c.wantSvcCalled {
				t.Fatalf("svc called = %v, want %v", svc.createCalled, c.wantSvcCalled)
			}
		})
	}
}

// ==== 测试 GET /:code ====
func TestRedirect(t *testing.T) {
	cases := []struct {
		name          string
		fakeURL       string
		fakeErr       error
		wantStatus    int
		wantBizCode   int
		wantRecordNum int // recorder 被调用的次数
	}{
		{"跳转成功", "https://go.dev", nil, 302, 0, 1}, // 302 响应没有业务码
		{"不存在", "", service.ErrNotFound, 404, 40401, 0},
		{"过期", "", service.ErrExpired, 410, 41001, 0},
		{"内部错误", "", errors.New("db down"), 500, 50000, 0},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			svc := &fakeService{resolveURL: c.fakeURL, resolveErr: c.fakeErr}
			rec := &fakeRecorder{}
			router := newTestRouter(svc, rec)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			router.ServeHTTP(w, req)

			if w.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, c.wantStatus)
			}
			if svc.lastCode != "test" {
				t.Fatalf("Resolve got code %q, want test", svc.lastCode)
			}

			// 验证 302 跳转的 Location 头
			if c.wantStatus == 302 {
				loc := w.Header().Get("Location")
				if loc != c.fakeURL {
					t.Fatalf("Location = %s, want %s", loc, c.fakeURL)
				}
			} else {
				var resp struct {
					Code int `json:"code"`
				}
				json.Unmarshal(w.Body.Bytes(), &resp)
				if resp.Code != c.wantBizCode {
					t.Fatalf("biz code = %d, want %d", resp.Code, c.wantBizCode)
				}
			}

			if rec.count != c.wantRecordNum {
				t.Fatalf("record called = %d times, want %d", rec.count, c.wantRecordNum)
			}
		})
	}
}
