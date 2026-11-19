# 注意：这里故意不写 `# syntax=docker/dockerfile:1`。
# 那条指令会让 BuildKit 去 docker.io 拉 docker/dockerfile 前端镜像；
# 在国内网络下会卡在 auth.docker.io 超时（实测 build 直接失败）。
# 本文件只用到 `RUN --mount=type=cache` 与多阶段构建，Docker 23+ 内置前端
# 已完全支持，不需要外部前端镜像。若你的网络能直连 Docker Hub 想启用
# 更新的语法特性，再把这行加回来。
#
# mygopeed —— 单镜像：gopeed-headless（REST API）+ Caddy（静态面板 + 鉴权反代）
#
# 在仓库根目录构建：
#   docker compose build            # 用 docker-compose.yml（通用）
#   docker compose -f docker-compose.nas.yaml build   # 绿联 NAS / host 网络
#
# 直接用 docker 构建：
#   docker build -t mygopeed:1.9.3-my .
# 给 OrangePi Zero3（arm64）交叉构建：
#   docker buildx build --platform linux/arm64 -t mygopeed:1.9.3-my --load .

ARG GO_IMAGE=golang:1.24.11-alpine3.23
ARG RUNTIME_IMAGE=alpine:3.23

# ---------- stage 1: 编译后端 ----------
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

ARG VERSION=1.9.3-my
# -buildvcs=false：镜像里没有 .git，避免 go 尝试 stamp VCS 信息
# -tags nosqlite：与上游 Dockerfile 保持一致（当前源码无该 tag 的构建约束，留着无副作用）
# 构建的是 cmd/gopeed-headless：它不嵌前端，绕开 cmd/web 的 //go:embed dist/* 缺失问题
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -tags nosqlite -buildvcs=false -trimpath \
      -ldflags="-s -w \
        -X github.com/GopeedLab/gopeed/pkg/base.Version=${VERSION} \
        -X github.com/GopeedLab/gopeed/pkg/base.InDocker=true" \
      -o /out/gopeed-headless ./cmd/gopeed-headless

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
    apk add --no-cache ca-certificates tzdata caddy su-exec \
 && mkdir -p /app/storage /app/etc /downloads /srv/gopeed-web

COPY --from=build /out/gopeed-headless /app/gopeed-headless
COPY gopeed-web/web/ /srv/gopeed-web/
COPY docker/config.json /app/etc/config.json
COPY docker/Caddyfile.tmpl /app/etc/Caddyfile.tmpl
COPY docker/entrypoint.sh /app/entrypoint.sh
# 静态产物在仓库里是 0770 root:root，而 caddy 以 PUID:PGID（默认非 root）运行，
# 会因 group/other 无读权限而返回 403 / 首页 404。这里统一放开为可读。
RUN chmod 0755 /app/entrypoint.sh /app/gopeed-headless \
 && chmod 0755 /srv/gopeed-web \
 && chmod 0644 /srv/gopeed-web/*

ENV TZ=Asia/Shanghai \
    PUID=0 \
    PGID=0 \
    UMASK=022 \
    PANEL_PORT=8090 \
    PANEL_USER=gopeed \
    PANEL_PASS= \
    PANEL_AUTH=on \
    API_PORT=9999 \
    DOWNLOAD_DIR=/downloads \
    MAX_RUNNING=3

VOLUME ["/app/storage", "/downloads"]
EXPOSE 8090
EXPOSE 51413/tcp
EXPOSE 51413/udp

WORKDIR /app
ENTRYPOINT ["/app/entrypoint.sh"]
