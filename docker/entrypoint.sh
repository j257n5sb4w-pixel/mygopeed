#!/bin/sh
# mygopeed 容器入口：单进程、单端口。
# 面板（wasm UI）已经 //go:embed 进 gopeed 二进制，与 REST API 共用一个端口，
# 所以这里不再需要 Caddy，也不需要再起第二个进程。
set -eu

log() { printf '%s [entrypoint] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"; }

umask "${UMASK:-022}"

PUID="${PUID:-0}"
PGID="${PGID:-0}"
PANEL_PORT="${PANEL_PORT:-8090}"
PANEL_ADDR="${PANEL_ADDR:-0.0.0.0}"
PANEL_USER="${PANEL_USER:-gopeed}"
PANEL_PASS="${PANEL_PASS:-}"
PANEL_AUTH="${PANEL_AUTH:-on}"
BT_LISTEN_PORT="${BT_LISTEN_PORT:-51413}"
DOWNLOAD_DIR="${DOWNLOAD_DIR:-/downloads}"
MAX_RUNNING="${MAX_RUNNING:-3}"

# 路径都可以覆盖，默认值就是镜像里的布局；覆盖只为了能脱离容器跑自测
STORAGE_DIR="${STORAGE_DIR:-/app/storage}"
ETC_DIR="${ETC_DIR:-/app/etc}"
GOPEED_BIN="${GOPEED_BIN:-/app/gopeed}"
SU_EXEC="${SU_EXEC:-su-exec}"

# ---------- 目录 ----------
# gopeed 不用 .part/.incomplete 中间文件，而是直接写目标路径再 rename
# （补丁里的 opts.rename 就是下载完成后改名），所以不需要另建临时目录
mkdir -p "$STORAGE_DIR" "$DOWNLOAD_DIR" "$ETC_DIR"

chown -R "$PUID:$PGID" "$STORAGE_DIR" 2>/dev/null || true
# 下载目录只改顶层，避免在几十万文件上做 -R
chown "$PUID:$PGID" "$DOWNLOAD_DIR" 2>/dev/null || true

# ---------- 面板口令 ----------
AUTH_ARGS=""
if [ "$PANEL_AUTH" = "on" ]; then
	if [ -z "$PANEL_PASS" ]; then
		PANEL_PASS=$(head -c 18 /dev/urandom | base64 | tr -d '\n=+/' | cut -c1-16)
		log "PANEL_PASS 未设置，本次随机生成（重启会变，建议在 compose 里写死）"
	fi
	log "面板鉴权已开启"
	log "  用户名 $PANEL_USER"
	log "  口令   $PANEL_PASS"
	AUTH_ARGS="-u $PANEL_USER -p $PANEL_PASS"
else
	log "警告：PANEL_AUTH=off，面板与 API 全部无鉴权，切勿把 $PANEL_PORT 暴露到公网"
fi

# ---------- config.json ----------
# 注意 gopeed 只在任务库首次初始化时读取这份配置，之后改它不生效，
# 要改请在面板「设置」里改（会落到 storage/gopeed.db）
sed -e "s|__DOWNLOAD_DIR__|${DOWNLOAD_DIR}|g" \
    -e "s/__BT_LISTEN_PORT__/${BT_LISTEN_PORT}/g" \
    -e "s/__MAX_RUNNING__/${MAX_RUNNING}/g" \
    "$ETC_DIR/config.json" > "$ETC_DIR/config.runtime.json"
log "下载目录 = ${DOWNLOAD_DIR}"
log "BT listenPort = ${BT_LISTEN_PORT}（需同时映射 TCP+UDP）"

log "面板与 API 共用端口：http://<本机IP>:$PANEL_PORT"
log "启动 $GOPEED_BIN（单进程，无 Caddy）"

# ---------- 启动 ----------
# 用 exec 替换掉 entrypoint 自身，让 gopeed 直接成为 PID 1：
# 它自己会处理 SIGTERM/SIGINT（Downloader.Close() 把任务暂停并落库），
# 所以 docker stop 能干净退出，不需要在 shell 里再转发信号。
# shellcheck disable=SC2086
exec "$SU_EXEC" "$PUID:$PGID" "$GOPEED_BIN" \
	-A "$PANEL_ADDR" -P "$PANEL_PORT" \
	$AUTH_ARGS \
	-d "$STORAGE_DIR" \
	-c "$ETC_DIR/config.runtime.json"
