FROM golang:1.27.0-alpine

WORKDIR /app

# 安装必要的工具
RUN apk add --no-cache git

# 复制go.mod和go.sum
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY . .

# 仅供后端目录独立开发构建；生产源码构建统一使用仓库根 Dockerfile。
RUN VERSION_VALUE="$(./scripts/resolve-version.sh)" && \
    go build -ldflags="-s -w -X main.Version=${VERSION_VALUE}" -o /app/sub2api ./cmd/server/ && \
    go build -ldflags="-s -w" -o /app/openai-persona-migrate ./cmd/openai-persona-migrate/

# 暴露端口
EXPOSE 8080

# 运行应用
CMD ["/app/sub2api"]
