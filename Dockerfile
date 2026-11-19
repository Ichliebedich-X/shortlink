# ---------- 构建阶段 ----------
FROM golang:1.25-alpine AS builder

WORKDIR /src

# 国内网络访问 proxy.golang.org 会失败。
# 容器不会继承宿主机的 GOPROXY，所以必须在镜像里显式设置。
# 这一行必须放在 go mod download 之前。
ENV GOPROXY=https://goproxy.cn,direct

# 先复制依赖文件，利用 Docker 层缓存：
# go.mod / go.sum 没变时，不会重复下载依赖。
COPY go.mod go.sum ./
RUN go mod download

# 再复制源码
COPY . .

# 静态编译：运行镜像里不需要 Go 工具链
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/shortlink .

# ---------- 运行阶段 ----------
FROM alpine:3.20

# ca-certificates: HTTPS 证书
# tzdata: 支持 Asia/Shanghai 时区
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 app

ENV TZ=Asia/Shanghai

WORKDIR /app
COPY --from=builder /out/shortlink /app/shortlink

# 不用 root 运行
USER app
EXPOSE 8080

ENTRYPOINT ["/app/shortlink"]
