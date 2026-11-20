# mygopeed

自用的一套 gopeed 定制方案，跑在一台 OrangePi Zero3（ARM64，Debian）上：

- **gopeed-patched/** —— gopeed v1.9.3 的**源码补丁版**（默认编 headless，也可编 `-tags web` 把面板内嵌进二进制）。在官方源码上叠了九类改动：
  下载完成后自动改名（`opts.rename`）、目录映射（`pathMappings`）、全局解压密码表（`archive.passwords`）、
  解压完成后延迟删压缩包、BT 删除时的空指针保护、做种自动清理（`seedAutoRemove`）、
  启动后自动续传（`resumeOnStart`）、代理只作用于 HTTP（`proxy.mode` = `off`/`all`/`partial` + `keywords`）、
  以及**移除 ed2k 协议**（FetcherManager 只留 http/bt）。
  完整 diff（17 个文件：8 改 5 增 4 删）见 [gopeed-patched/local-patches.patch](gopeed-patched/local-patches.patch)，
  细节见 [gopeed-patched/README.md](gopeed-patched/README.md)。
- **gopeed-web/** —— 用 [MyGo](https://github.com/egoist/mygo)（Go → WebAssembly）写的 gopeed 面板，
  编译成 `app.wasm`（`web/` 目录里带着构建好的产物），构建镜像时被 `//go:embed` 编进 gopeed 二进制，
  与 REST API 共用同一个端口，不再需要 Caddy 反代。
  功能：任务列表/详情/暂停/删除/设置/解压密码，并适配了上面补丁版的 `rename` 能力。
  细节见 [gopeed-web/README.md](gopeed-web/README.md)。
- **gopeed-push/** —— **已停用**的推送网关存档。它的三件事（下载后改名、删到改名后文件、目录映射）
  已经做进 gopeed-patched 本体，这里只留源码与说明做历史参考。
  见 [gopeed-push/README.md](gopeed-push/README.md)。

## 布局

```
mygopeed/
├── Dockerfile               # 单镜像、单进程、单端口（面板已内嵌）
├── docker-compose.yml       # 通用部署（bridge 网络 + 端口映射）
├── docker-compose.nas.yaml  # 绿联 NAS 变体（host 网络，见下文「Docker」）
├── docker/                  # 容器启动脚本与默认配置
│   ├── entrypoint.sh        #   下发 config.json、exec 直起 gopeed（单进程）
│   └── config.json          #   首次启动灌入的下载器配置
├── gopeed-patched/          # gopeed 补丁版源码 + 补丁文件 + 构建说明
│   ├── gd/                  #   补丁后的完整源码树（基于 GopeedLab/gopeed v1.9.3）
│   ├── local-patches.patch  #   对官方 v1.9.3 的完整 diff（patch -p1 可直接打）
│   └── README.md            #   每个补丁的原理、规则、构建部署命令
├── gopeed-web/              # MyGo 写的 wasm 面板
│   ├── main.go client.go    #   面板源码
│   ├── web/                 #   构建产物（app.wasm、index.html、wasm_exec.js 及 .gz）
│   ├── caddy-gopeed.conf    #   旧双进程方案的反代样例（现已不需要）
│   └── README.md            #   功能、构建、部署说明
└── gopeed-push/             # 已停用的推送网关（存档）
```

## Docker

**一个镜像、一个容器、一个端口。** 面板（wasm UI）用 `//go:embed` 编进了
gopeed 二进制，UI 与 REST API 共用同一个监听端口 `8090`，容器里只有一个进程，
不需要 Caddy，也不再额外占一个 API 端口。

```bash
# 首次启动前把 docker-compose.yml 里的 PANEL_PASS 改成自己的口令
docker compose build
docker compose up -d
# 浏览器打开 http://<主机IP>:8090，面板和 API 都是这个端口
```

构建约 2 分钟（依赖缓存命中后几秒）。映射关系：

| 宿主 | 容器 | 说明 |
|---|---|---|
| `./data/storage` | `/app/storage` | 任务库 + 日志，删了就丢全部任务 |
| `./data/downloads` | `/downloads` | 默认下载目录，新建任务时路径留空即落这里 |

想再挂媒体库就自己加一行，例如 `- /mnt/media:/media`；
新建任务时路径填 `/media` 即写入该目录。

### 单端口方案改了哪 4 处源码

上游的鉴权中间件会让内嵌面板打不开，所以必须带上这几处补丁：

1. **`pkg/rest/server.go`** —— 新增 `panelKey = base64(用户名:口令)`，
   并在中间件里认 `X-Panel-Key`：非 `/api/` 路径本来就直接放行（静态资源
   必须免鉴权，否则首页 401），`/api/*` 除了原有 Bearer token 外也接受
   这个头。同时给 CORS 的 `AllowedHeaders` 加上 `"X-Panel-Key"`。
2. **`pkg/rest/web.go`**（新增）—— `webStaticHandler` 优先发同名 `.gz`
   预压缩文件（`app.wasm` 20MB → 5MB），并把 `.wasm` 的 `Content-Type`
   修正为 `application/wasm`（`WebAssembly.instantiateStreaming` 强制要求）。
3. **`pkg/rest/gizp_middleware.go`** —— 上游实现无条件加
   `Content-Encoding: gzip`，但 `http.FileServer` 可能已按未压缩长度写了
   `Content-Length`，会导致响应截断或挂起。重写后 `WriteHeader` 先删
   `Content-Length`，HEAD 请求不加压缩头。
4. **`cmd/web/main.go`** —— `//go:embed dist/*` 改成 `all:dist`，并加启动
   横幅，明确打印「面板与 API 共用端口」。

### 几个必须知道的设计取舍

**为什么现在编 `cmd/web` 而不是 `cmd/gopeed-headless`。**
`cmd/web/main.go` 有 `//go:embed dist/*`，而 `cmd/web/dist/` 被
`gd/.gitignore` 排除、仓库里并不存在，直接构建会报
`pattern dist/*: no matching files found`。Dockerfile 里先
`COPY gopeed-web/web/ /src/cmd/web/dist/` 再 `go build -tags web` 即可。
这样面板才是真正「原生自带」：同一份二进制、同一个端口、没有反代。

代价是二进制含面板产物（约 66MB，headless 版 40MB），换掉的是整个
Caddy 进程和第二个端口。

**为什么不能再开 `ApiToken`。**
后端如果设了 `ApiToken`，中间件会连 `/` 和 `/app.wasm` 这类静态请求一起
返 401 —— 浏览器加载首页时不会带 `X-Api-Token`，面板直接打不开。
内嵌面板只用 `X-Panel-Key = base64(用户名:口令)`（见
`gopeed-web/main.go` 的 `doLogin()`），所以鉴权只能走这条路。

**为什么 `docker/config.json` 要包一层 `downloadConfig`。**
`cmd/web` 的 `-c` 解析的是 `cmd/web/flags.go` 里的 `args` 结构体，
`DownloadConfig` 在它下面、json tag 是 `downloadConfig`；
而 `cmd/gopeed-headless` 的 `-c` 直接解析 `base.DownloaderStoreConfig`。
两种格式不通用，换方案时必须改文件结构。

**为什么 `docker/config.json` 要写满所有字段。**
`pkg/util/json.go` 的 `MapToStruct` 是 Marshal 之后再 Unmarshal，
缺字段会被置零而不是保留默认值；而 `Setup()` 只在 `protocolConfig[bt]`
这个键**不存在**时才补默认。所以只写 `listenPort` 会导致 `seedRatio`
之类的字段全变 0。

**配置只在首次启动生效。**
`-c` 由 `cmd/server.go` 的 `if downloadCfg.FirstLoad` 分支处理，
库里已有 config 就不再应用。改完 `docker/config.json` 要生效，
得停容器、删 `data/storage/gopeed.db`、再启动；日常改配置走面板设置页。

### 绿联 NAS（UGOS）

用 `docker-compose.nas.yaml`，它和通用版的差别都是被环境逼的：

1. **`network_mode: host`** —— UGOS 把 `net.ipv4.ip_forward` 设成 0，
   docker 的 bridge 网络完全没有出网能力（容器里连 `223.5.5.5:53` 都超时，
   apk / Go module 全挂）。`build.network: host` 同理，否则构建期拉包失败。
   `/volume1/docker` 下已有的 ddns-go、v2raya 也都是 host 网络。
2. **构建走国内源** —— daocloud 镜像 + `goproxy.cn` + `mirrors.aliyun.com`。

单端口之后不再有「宿主 9999 被 UGOS nginx 占用」的问题（旧版为此要把
API 挪到 19999），`API_PORT` 这个环境变量已删除。

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

单文件内含面板（面板与 API 同端口，无 Caddy）：

```bash
cd gopeed-patched/gd
# 面板产物不在 git 里（cmd/web/dist 被 .gitignore 排除），必须自己放
mkdir -p cmd/web/dist && cp -r ../../gopeed-web/web/* cmd/web/dist/
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
go build -tags "web" -buildvcs=false -trimpath \
  -ldflags "-s -w -X github.com/GopeedLab/gopeed/pkg/base.Version=1.9.3-my \
            -X github.com/GopeedLab/gopeed/pkg/base.InDocker=true" \
  -o gopeed-linux-arm64 ./cmd/web
# 运行：-u/-p 开面板鉴权，-P 是 UI 和 API 共用的端口
./gopeed-linux-arm64 -A 0.0.0.0 -P 8090 -u gopeed -p '你的口令' -d ./storage
```

web 面板（浏览器 wasm）：

```bash
cd gopeed-web
GOOS=js GOARCH=wasm go build -o web/app.wasm .
# 注意：绝对不要加 -trimpath，加了面板会永久停在「正在加载…」
# 然后 gzip（README 里有现成脚本），连同 index.html / wasm_exec.js 一起
# 放进 gopeed-patched/gd/cmd/web/dist/ 供 //go:embed 使用
```

## 敏感信息说明

仓库里的口令、API token 均已替换为 `<your-password>` / `<your-gopeed-api-token>` 占位符，
内网 IP 写成了 `192.0.2.10`（RFC 5737 文档地址）或 `<your-server-ip>`。
实际部署时请替换成自己的值。

## 许可

- `gopeed-patched/gd/` 内的 gopeed 源码遵循其原仓库的 **GPL-3.0** 许可（见 gd/LICENSE），
  补丁部分同样按 GPL-3.0 分发。
- `gopeed-web/`（MyGo 面板）与 `gopeed-push/`（网关存档）为本仓库自有代码，MIT 许可。
