// Command shortlink 启动短链接服务，并负责依赖装配和优雅关闭。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"shortlink/internal/cache"
	"shortlink/internal/config"
	"shortlink/internal/dao"
	"shortlink/internal/handler"
	"shortlink/internal/logger"
	"shortlink/internal/middleware"
	"shortlink/internal/service"
	"shortlink/internal/stats"
	"shortlink/pkg/base62"
	"shortlink/pkg/bloom"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func main() {
	// 0. 设置全局默认 Logger，之后全项目直接 slog.Info 就会输出 JSON
	slog.SetDefault(logger.New())

	// 1. 加载配置
	cfg := config.Load()

	// 2. 初始化 MySQL 连接
	// TranslateError: 让 GORM 把 MySQL 的原生错误翻译成统一错误类型。
	// 例如唯一键冲突（MySQL 错误码 1062）会被翻译成 gorm.ErrDuplicatedKey，
	// 这样 service 层判断"短码重复"时，不用直接依赖 MySQL 的错误码。
	db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{
		TranslateError: true,
	})
	if err != nil {
		// 把 log.Fatalf 换成 slog.Error，最后自己手动 os.Exit(1)
		// 因为 slog 没有 Fatalf 方法，我们要打印结构化日志并退出
		slog.Error("连接MySQL失败", "err", err)
		os.Exit(1)
	}

	// 3. 初始化 Redis 连接
	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.RedisAddr,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
		MaxRetries:   1,
	})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		slog.Error("连接Redis失败", "err", err)
		os.Exit(1) // main 里直接 return 的退出码是 0，监控会误以为服务正常结束
	}
	defer rdb.Close()

	// 4. 组装依赖
	// 初始化 DAO 和 Collector
	linkDao := dao.NewLinkDao(db)
	statsDao := dao.NewStatsDao(db)

	// ================= 【布隆过滤器初始化与重建】 =================
	// 1. 初始化过滤器。key 叫做 "shortlink:bloom:codes"，存放在 Redis 里共享。
	bloomFilter := bloom.New(rdb, "shortlink:bloom:codes")

	// 2. 从数据库捞出全量合法的短码。
	// 必须在启动时捞一次，否则 Redis 重启后，老短链全被当成“不存在”。
	codes, err := linkDao.ListAllCodes(context.Background())
	if err != nil {
		slog.Error("加载全量短码失败", "err", err)
		os.Exit(1)
	}

	// 3. 重建位图。
	// 【核心注意】Rebuild 内部用的是临时 key + RENAME 的原子操作。
	// 千万不要用“先 DEL 后 ADD”的方式，否则在重建的那几十毫秒里，所有流量都会 404。
	if err := bloomFilter.Rebuild(context.Background(), codes); err != nil {
		slog.Error("重建布隆过滤器失败", "err", err)
		os.Exit(1)
	}
	slog.Info("布隆过滤器就绪", "codes", len(codes))
	// ==============================================================

	// 4. 校准发号器：如果 Redis 计数器被清空或回退，
	// 从已有短码的最大编号重新抬起，避免从 1 开始撞已有记录。
	if err := calibrateCounter(context.Background(), rdb, codes); err != nil {
		slog.Error("发号器校准失败", "err", err)
		os.Exit(1)
	}

	// 创建收集器，缓冲区大小设为 1000
	collector := stats.NewCollector(statsDao, 1000)

	// 核心：启动后台打工人
	// 用可取消的 ctx 启动，优雅关闭时可以通知它刷完盘子再退出
	statsCtx, stopStats := context.WithCancel(context.Background())
	go collector.Run(statsCtx)

	// 把组装好的 bloomFilter 交给 Service
	redisCache := cache.NewRedis(rdb)
	linkSvc := service.NewLinkService(linkDao, redisCache, bloomFilter)
	linkHandler := handler.NewLinkHandler(linkSvc, collector)

	// 5. 配置 Gin 路由
	r := gin.New()
	// 挂载自定义日志中间件和 Recovery 中间件
	r.Use(middleware.RequestLogger(slog.Default()), gin.Recovery())

	// 健康检查：部署时给 Docker / 负载均衡用
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	r.POST("/api/v1/links", linkHandler.CreateLink)
	r.GET("/:code", linkHandler.Redirect)

	// 6. 启动 HTTP 服务（带优雅关闭）
	srv := &http.Server{
		Addr:    ":8080",
		Handler: r,
	}

	// 监听 Ctrl+C 和容器里的 SIGTERM
	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("服务器启动在：8080端口")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("启动失败", "err", err)
			os.Exit(1)
		}
	}()

	<-shutdownCtx.Done()
	slog.Info("收到退出信号，开始优雅关闭")

	// 第 1 步：停止接收新请求，等待正在处理的请求结束
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("HTTP 优雅关闭失败", "err", err)
	}

	// 第 2 步：通知统计 worker 退出；它会先把 batch 刷盘
	stopStats()
	collector.Wait()

	// 第 3 步：关闭数据库连接
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}

	slog.Info("服务已退出")
}

// calibrateCounter 把 Redis 计数器校准到不小于已有最大短码编号。
// Lua 保证“读当前值、比较、设置”是原子的，而且只抬高、不降低。
func calibrateCounter(ctx context.Context, rdb *redis.Client, codes []string) error {
	var maxID uint64
	for _, code := range codes {
		id, err := base62.Decode(code)
		if err != nil {
			continue // 以后如果支持自定义短码，跳过非 base62 的码
		}
		if id > maxID {
			maxID = id
		}
	}

	script := redis.NewScript(`
local cur = tonumber(redis.call('GET', KEYS[1]) or '0')
local want = tonumber(ARGV[1])
if want > cur then
    redis.call('SET', KEYS[1], want)
    return want
end
return cur
`)

	if err := script.Run(ctx, rdb, []string{"shortlink:id"}, maxID).Err(); err != nil {
		return err
	}
	slog.Info("发号器校准完成", "max_code_id", maxID)
	return nil
}
