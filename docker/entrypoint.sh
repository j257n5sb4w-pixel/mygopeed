#!/bin/sh
# mygopeed 容器入口：生成 Caddyfile → 起 gopeed-headless → 起 Caddy → 互相监督
set -eu

log() { printf '%s [entrypoint] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"; }

umask "${UMASK:-022}"

PUID="${PUID:-0}"
PGID="${PGID:-0}"
PANEL_PORT="${PANEL_PORT:-8090}"
API_PORT="${API_PORT:-9999}"
PANEL_USER="${PANEL_USER:-gopeed}"
PANEL_PASS="${PANEL_PASS:-}"
PANEL_AUTH="${PANEL_AUTH:-on}"
BT_LISTEN_PORT="${BT_LISTEN_PORT:-51413}"
DOWNLOAD_DIR="${DOWNLOAD_DIR:-/downloads}"
MAX_RUNNING="${MAX_RUNNING:-3}"

# 路径都可以覆盖，默认值就是镜像里的布局；覆盖只为了能脱离容器跑自测
STORAGE_DIR="${STORAGE_DIR:-/app/storage}"
ETC_DIR="${ETC_DIR:-/app/etc}"
CADDY_HOME="${CADDY_HOME:-/app/caddy}"
GOPEED_BIN="${GOPEED_BIN:-/app/gopeed-headless}"
SU_EXEC="${SU_EXEC:-su-exec}"

# alpine 的 caddy 包把二进制装在 /usr/bin/caddy，个别版本在 /usr/sbin
CADDY_BIN="${CADDY_BIN:-}"
if [ -z "$CADDY_BIN" ]; then
	for c in /usr/bin/caddy /usr/sbin/caddy /usr/local/bin/caddy; do
		if [ -x "$c" ]; then CADDY_BIN="$c"; break; fi
	done
fi
if [ -z "$CADDY_BIN" ] || [ ! -x "$CADDY_BIN" ]; then
	log "找不到 caddy 可执行文件（试过 /usr/bin /usr/sbin /usr/local/bin），镜像构建有问题"
	exit 1
fi

# ---------- 目录 ----------
# gopeed 不用 .part/.incomplete 中间文件，而是直接写目标路径再 rename
# （补丁里的 opts.rename 就是下载完成后改名），所以不需要另建临时目录
mkdir -p "$STORAGE_DIR" "$DOWNLOAD_DIR" "$ETC_DIR"
mkdir -p "$CADDY_HOME/data" "$CADDY_HOME/config"

chown -R "$PUID:$PGID" "$STORAGE_DIR" "$CADDY_HOME" 2>/dev/null || true
# 下载目录只改顶层，避免在几十万文件上做 -R
chown "$PUID:$PGID" "$DOWNLOAD_DIR" 2>/dev/null || true

# ---------- 面板口令 ----------
if [ "$PANEL_AUTH" = "on" ]; then
	if [ -z "$PANEL_PASS" ]; then
		PANEL_PASS=$(head -c 18 /dev/urandom | base64 | tr -d '\n=+/' | cut -c1-16)
		log "PANEL_PASS 未设置，本次随机生成"
		log "  用户名 $PANEL_USER"
		log "  口令   $PANEL_PASS"
		log "要固定口令：设置环境变量 PANEL_PASS 后 docker compose up -d --force-recreate"
	fi
	PANEL_KEY=$(printf '%s:%s' "$PANEL_USER" "$PANEL_PASS" | base64 | tr -d '\n')
	log "面板鉴权已开启（面板登录用上面的用户名+口令）"
else
	PANEL_KEY=""
	log "警告：PANEL_AUTH=off，面板与 API 无鉴权，切勿把 $PANEL_PORT 暴露到公网"
fi

# ---------- config.json ----------
# 注意 gopeed 只在任务库首次初始化时读取这份配置，之后改它不生效，
# 要改请在面板「设置」里改（会落到 storage/gopeed.db）
sed "s/__BT_LISTEN_PORT__/${BT_LISTEN_PORT}/g" "$ETC_DIR/config.json" > "$ETC_DIR/config.runtime.json"
log "BT listenPort = ${BT_LISTEN_PORT}（需同时映射 TCP+UDP）"

# ---------- Caddyfile ----------
# 只替换「整行就是占位标记」的行，注释里出现同名标记不会被误替换
awk -v port="$PANEL_PORT" -v apiport="$API_PORT" -v key="$PANEL_KEY" -v auth="$PANEL_AUTH" '
{
	line = $0
	gsub(/__PANEL_PORT__/, port, line)
	gsub(/__API_PORT__/, apiport, line)
	if (line ~ /^[ \t]*__AUTH_BLOCK__[ \t]*$/) {
		if (auth == "on") {
			printf "\t\t@denied not header X-Panel-Key \"%s\"\n", key
			print  "\t\trespond @denied \"unauthorized\" 401"
		}
	} else {
		print line
	}
}' "$ETC_DIR/Caddyfile.tmpl" > "$ETC_DIR/Caddyfile"

# ---------- 启动 ----------
PIDS=""

stop_children() {
	for p in $PIDS; do kill -TERM "$p" 2>/dev/null || true; done
	# 最多等 10 秒让 gopeed 把任务 pause 并落库，超时再 KILL
	i=0
	while [ "$i" -lt 10 ]; do
		alive=0
		for p in $PIDS; do
			if kill -0 "$p" 2>/dev/null; then
				alive=1
			fi
		done
		if [ "$alive" -eq 0 ]; then
			break
		fi
		i=$((i + 1))
		sleep 1
	done
	for p in $PIDS; do kill -KILL "$p" 2>/dev/null || true; done
	for p in $PIDS; do wait "$p" 2>/dev/null || true; done
}

on_signal() {
	log "收到停止信号，正在优雅退出…"
	stop_children
	log "已退出"
	exit 0
}
trap on_signal TERM INT

# 后端只监听回环：对外一律经过 Caddy，鉴权在 Caddy 侧
"$SU_EXEC" "$PUID:$PGID" "$GOPEED_BIN" \
	-A 127.0.0.1 -P "$API_PORT" \
	-d "$STORAGE_DIR" \
	-download-dir "$DOWNLOAD_DIR" \
	-max-running "$MAX_RUNNING" \
	-c "$ETC_DIR/config.runtime.json" &
GOPEED_PID=$!
PIDS="$PIDS $GOPEED_PID"
log "gopeed-headless 已启动（pid $GOPEED_PID，监听 127.0.0.1:$API_PORT）"

HOME="$CADDY_HOME" XDG_DATA_HOME="$CADDY_HOME/data" XDG_CONFIG_HOME="$CADDY_HOME/config" \
	"$SU_EXEC" "$PUID:$PGID" "$CADDY_BIN" run \
	--config "$ETC_DIR/Caddyfile" --adapter caddyfile &
CADDY_PID=$!
PIDS="$PIDS $CADDY_PID"
log "caddy 已启动（pid $CADDY_PID，监听 :$PANEL_PORT）"
log "面板地址：http://<本机IP>:$PANEL_PORT"

# 任一方意外退出就整体退出，交给 compose 的 restart 策略
while :; do
	if ! kill -0 "$GOPEED_PID" 2>/dev/null; then
		log "gopeed-headless 意外退出，容器即将重启"
		stop_children
		exit 1
	fi
	if ! kill -0 "$CADDY_PID" 2>/dev/null; then
		log "caddy 意外退出，容器即将重启"
		stop_children
		exit 1
	fi
	sleep 3
done
