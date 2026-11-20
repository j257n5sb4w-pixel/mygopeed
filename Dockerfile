# 注意：这里故意不写 `# syntax=docker/dockerfile:1`。
# 那条指令会让 BuildKit 去 docker.io 拉 docker/dockerfile 前端镜像；
# 在国内网络下会卡在 auth.docker.io 超时（实测 build 直接失败）。
# 本文件只用到 `RUN --mount=type=cache` 与多阶段构建，Docker 23+ 内置前端
# 已完全支持，不需要外部前端镜像。若你的网络能直连 Docker Hub 想启用
# 更新的语法特性，再把这行加回来。
#
# mygopeed —— 单进程、单端口。
#
# 面板（mygopeed 的 wasm UI）被 //go:embed 编进 gopeed 二进制，
# 与 REST API 共用同一个监听端口，因此不再需要 Caddy/nginx 反代，
# 也不会再额外占一个端口。
#
# 把本目录内容按下面的布局放进仓库根目录，然后在仓库根目录执行：
#   Dockerfile            -> 仓库根/Dockerfile
#   docker-compose.yml    -> 仓库根/docker-compose.yml
#   .dockerignore         -> 仓库根/.dockerignore
#   docker/entrypoint.sh  -> 仓库根/docker/entrypoint.sh
#   docker/config.json    -> 仓库根/docker/config.json
#
# 构建：
#   docker build -t mygopeed:1.9.3-my .
# 给 OrangePi Zero3（arm64）交叉构建：
#   docker buildx build --platform linux/arm64 -t mygopeed:1.9.3-my --load .

ARG GO_IMAGE=golang:1.24.11-alpine3.23
ARG RUNTIME_IMAGE=alpine:3.23

# ---------- stage 1: 编译（二进制内含面板） ----------
FROM ${GO_IMAGE} AS build
WORKDIR /src

# CGO_ENABLED=0：纯静态二进制，运行时不依赖 musl/glibc
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOFLAGS=-mod=mod

# 国内网络构建时用：--build-arg GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

# 先只拷依赖清单：改源码时这一层仍命中缓存
COPY gopeed-patched/gd/go.mod gopeed-patched/gd/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY gopeed-patched/gd/ ./

# cmd/web/main.go 里有 `//go:embed all:dist`，dist 目录被 gd/.gitignore 排除，
# 所以仓库里没有它。这里把面板构建产物拷进去，再编 -tags web。
COPY gopeed-web/web/ /src/cmd/web/dist/

ARG VERSION=1.9.3-my
# -buildvcs=false：镜像里没有 .git，避免 go 尝试 stamp VCS 信息
# -tags web：启用 cmd/web/main.go，把面板嵌进二进制并在同端口提供
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -tags web -buildvcs=false -trimpath \
      -ldflags="-s -w \
        -X github.com/GopeedLab/gopeed/pkg/base.Version=${VERSION} \
        -X github.com/GopeedLab/gopeed/pkg/base.InDocker=true" \
      -o /out/gopeed ./cmd/web

# ---------- stage 2: 运行时 ----------
FROM ${RUNTIME_IMAGE}

# 留空 = 用官方源 dl-cdn.alpinelinux.org；国内可传 mirrors.aliyun.com 等
ARG APK_MIRROR=

RUN set -eux; \
    # apk 软件源可换国内镜像（默认 dl-cdn.alpinelinux.org，国内经常 DNS 抖动）
    # 传 --build-arg APK_MIRROR=mirrors.aliyun.com 即可切换，留空则用官方源。
    if [ -n "${APK_MIRROR:-}" ]; then \
      sed -i "s|dl-cdn.alpinelinux.org|${APK_MIRROR}|g" /etc/apk/repositories; \
    fi; \
    apk add --no-cache ca-certificates tzdata su-exec \
 && mkdir -p /app/storage /app/etc /downloads

COPY --from=build /out/gopeed /app/gopeed
COPY docker/config.json /app/etc/config.json
COPY docker/entrypoint.sh /app/entrypoint.sh

RUN chmod 0755 /app/entrypoint.sh /app/gopeed

ENV TZ=Asia/Shanghai \
    PUID=0 \
    PGID=0 \
    UMASK=022 \
    PANEL_PORT=8090 \
    PANEL_USER=gopeed \
    PANEL_PASS= \
    PANEL_AUTH=on \
    DOWNLOAD_DIR=/downloads \
    MAX_RUNNING=3 \
    BT_LISTEN_PORT=51413

VOLUME ["/app/storage", "/downloads"]
# 面板与 API 共用这一个端口
EXPOSE 8090
EXPOSE 51413/tcp
EXPOSE 51413/udp

WORKDIR /app
ENTRYPOINT ["/app/entrypoint.sh"]
