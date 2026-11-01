# 拾页笔记服务端容器镜像：多阶段构建，运行层只保留静态二进制与非 root 用户。
# 运行示例见 README 的「容器化本地运行」章节。

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gleannote ./cmd/api

FROM alpine:3.21
# 内存上限与 docs/ARCHITECTURE.md 一致：软上限 300MiB，常规 GC 目标 100
ENV GOGC=100 \
	GOMEMLIMIT=300MiB
RUN apk add --no-cache ca-certificates tzdata \
	&& addgroup -g 10001 app \
	&& adduser -D -H -u 10001 -G app app
COPY --from=build /out/gleannote /usr/local/bin/gleannote
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/gleannote"]
