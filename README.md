# mygopeed

自用的一套 gopeed 定制方案，跑在一台 OrangePi Zero3（ARM64，Debian）上：

- **gopeed-patched/** —— gopeed v1.9.3 的**源码补丁版**（headless，不嵌官方前端）。在官方源码上叠了九类改动：
  下载完成后自动改名（`opts.rename`）、目录映射（`pathMappings`）、全局解压密码表（`archive.passwords`）、
  解压完成后延迟删压缩包、BT 删除时的空指针保护、做种自动清理（`seedAutoRemove`）、
  启动后自动续传（`resumeOnStart`）、代理只作用于 HTTP（`proxy.mode` = `off`/`all`/`partial` + `keywords`）、
  以及**移除 ed2k 协议**（FetcherManager 只留 http/bt）。
  完整 diff（17 个文件：8 改 5 增 4 删）见 [gopeed-patched/local-patches.patch](gopeed-patched/local-patches.patch)，
  细节见 [gopeed-patched/README.md](gopeed-patched/README.md)。
- **gopeed-web/** —— 用 [MyGo](https://github.com/egoist/mygo)（Go → WebAssembly）写的 gopeed 面板，
  编译成 `app.wasm` 后由 Caddy 静态托管（`web/` 目录里带着构建好的产物）。
  功能：任务列表/详情/暂停/删除/设置/解压密码，并适配了上面补丁版的 `rename` 能力。
  细节见 [gopeed-web/README.md](gopeed-web/README.md)。
- **gopeed-push/** —— **已停用**的推送网关存档。它的三件事（下载后改名、删到改名后文件、目录映射）
  已经做进 gopeed-patched 本体，这里只留源码与说明做历史参考。
  见 [gopeed-push/README.md](gopeed-push/README.md)。

## 布局

```
mygopeed/
├── Dockerfile               # 单镜像：gopeed-headless + Caddy 双进程
├── docker-compose.yml       # 通用部署（bridge 网络 + 端口映射）
├── docker-compose.nas.yaml  # 绿联 NAS 变体（host 网络，见下文「Docker」）
├── docker/                  # 容器启动脚本与模板
│   ├── entrypoint.sh        #   生成 Caddyfile、下发 config.json、监督双进程
│   ├── Caddyfile.tmpl       #   面板静态站 + /api 反代 + X-Panel-Key 鉴权
│   └── config.json          #   首次启动灌入的下载器配置
├── gopeed-patched/          # gopeed 补丁版源码 + 补丁文件 + 构建说明
│   ├── gd/                  #   补丁后的完整源码树（基于 GopeedLab/gopeed v1.9.3）
│   ├── local-patches.patch  #   对官方 v1.9.3 的完整 diff（patch -p1 可直接打）
│   └── README.md            #   每个补丁的原理、规则、构建部署命令
├── gopeed-web/              # MyGo 写的 wasm 面板
│   ├── main.go client.go    #   面板源码
│   ├── web/                 #   构建产物（app.wasm、index.html、wasm_exec.js 及 .gz）
│   ├── caddy-gopeed.conf    #   Caddy 反代配置样例
│   └── README.md            #   功能、构建、部署说明
└── gopeed-push/             # 已停用的推送网关（存档）
```

## Docker

一条命令把后端和面板打包成单个镜像，容器内跑两个进程：
`gopeed-headless`（REST API，只听 127.0.0.1）+ Caddy（静态面板 + `/api` 反代）。
对外只暴露 8090，鉴权由 Caddy 校验 `X-Panel-Key`。

```bash
docker compose build
# 首次启动前把 docker-compose.yml 里的 PANEL_PASS 改成自己的口令
docker compose up -d
# 浏览器打开 http://<主机IP>:8090
```

体积约 202MB，构建 2 分钟左右（依赖缓存命中后 5 秒）。映射关系：

| 宿主 | 容器 | 说明 |
|---|---|---|
| `./data/storage` | `/app/storage` | 任务库 + 日志，删了就丢全部任务 |
| `./data/downloads` | `/downloads` | 默认下载目录，新建任务时路径留空即落这里 |

想再挂媒体库就自己加一行，例如 `- /mnt/media:/media`；
新建任务时路径填 `/media` 即写入该目录。

### 几个必须知道的设计取舍

**为什么编 `cmd/gopeed-headless` 而不是 `cmd/web`。**
`cmd/web/main.go:17` 有 `//go:embed dist/*`，而 `cmd/web/dist/` 被
`.gitignore` 排除了，直接构建会报 `pattern dist/*: no matching files found`。
headless 版本不嵌前端，绕开这个坑，二进制也从 55MB 降到 38MB。

**为什么鉴权放在 Caddy。**
后端如果设了 `ApiToken`，`pkg/rest/server.go` 的中间件会连 `/` 和
`/app.wasm` 这类静态请求一起返 401 —— 浏览器加载首页时不会带
`X-Api-Token`，面板直接打不开。而面板只认 `X-Panel-Key`、从不调用
`/api/web/login`，所以正确做法是后端只听 127.0.0.1 不开鉴权，
前面挂 Caddy 做 `X-Panel-Key = base64(用户名:口令)` 校验。

**为什么 `docker/config.json` 要写满所有字段。**
`pkg/util/json.go` 的 `MapToStruct` 是 Marshal 之后再 Unmarshal，
缺字段会被置零而不是保留默认值；而 `Setup()` 只在 `protocolConfig[bt]`
这个键**不存在**时才补默认。所以只写 `listenPort` 会导致 `seedRatio`
之类的字段全变 0。

**配置只在首次启动生效。**
`-c` / `-download-dir` / `-max-running` 由 `cmd/server.go` 的
`if downloadCfg.FirstLoad` 分支处理，库里已有 config 就不再应用。
改完 `docker/config.json` 要生效，得停容器、删 `data/storage/gopeed.db`、
再启动；日常改配置走面板设置页。

### 绿联 NAS（UGOS）

用 `docker-compose.nas.yaml`，它和通用版的差别只有三处，都是被环境逼的：

1. **`network_mode: host`** —— UGOS 把 `net.ipv4.ip_forward` 设成 0，
   docker 的 bridge 网络完全没有出网能力（容器里连 `223.5.5.5:53` 都超时，
   apk / Go module 全挂）。`build.network: host` 同理，否则构建期拉包失败。
   `/volume1/docker` 下已有的 ddns-go、v2raya 也都是 host 网络。
2. **`API_PORT: 19999`** —— 宿主 9999 被 UGOS 自带 nginx 常年占用。
3. **构建走国内源** —— daocloud 镜像 + `goproxy.cn` + `mirrors.aliyun.com`。

另外 Dockerfile 第一行**故意**没写 `# syntax=docker/dockerfile:1`：
那条指令会让 BuildKit 去 `auth.docker.io` 拉前端镜像，国内网络下直接
build 失败。本文件只用到 `RUN --mount=type=cache` 和多阶段构建，
Docker 23+ 内置前端完全够用。

## 快速构建

gopeed 补丁版（Linux ARM64，headless）：

```bash
cd gopeed-patched/gd
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
go build -tags "nosqlite" -trimpath \
  -ldflags "-s -w -X github.com/GopeedLab/gopeed/pkg/base.Version=1.9.3-headless" \
  -o gopeed-linux-arm64 ./cmd/gopeed-headless
```

web 面板（浏览器 wasm）：

```bash
cd gopeed-web
GOOS=js GOARCH=wasm go build -o web/app.wasm .
# 然后 gzip（README 里有现成脚本），连同 index.html / wasm_exec.js 一起扔到 Caddy 的静态目录
```

## 敏感信息说明

仓库里的口令、API token 均已替换为 `<your-password>` / `<your-gopeed-api-token>` 占位符，
内网 IP 写成了 `192.0.2.10`（RFC 5737 文档地址）或 `<your-server-ip>`。
实际部署时请替换成自己的值。

## 许可

- `gopeed-patched/gd/` 内的 gopeed 源码遵循其原仓库的 **GPL-3.0** 许可（见 gd/LICENSE），
  补丁部分同样按 GPL-3.0 分发。
- `gopeed-web/`（MyGo 面板）与 `gopeed-push/`（网关存档）为本仓库自有代码，MIT 许可。
