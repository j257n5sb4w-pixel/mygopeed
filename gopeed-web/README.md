# Gopeed Web UI（MyGo / WebAssembly）

用 [MyGo](https://mygo.egoist.dev) 的**原生 Go UI** 写的 Gopeed 客户端，编译成 WebAssembly，
浏览器直接打开 IP 就能用。界面由 Go 绘制（WebGL2 canvas），没有 HTML 前端框架。

**访问地址：<http://192.0.2.10:8090/>**

---

## 它是怎么工作的

```
浏览器
  │  加载 index.html + wasm_exec.js + app.wasm（约 20 MB，gzip 后 5 MB）
  ▼
MyGo 应用（Go → wasm）
  │  用 net/http（wasm 下走 fetch）调 REST
  ▼
Caddy :8090 ──/api/*──▶ 反向代理 ──▶ gopeed 127.0.0.1:9999
        └──其他路径──▶ /srv/gopeed-web 静态文件
```

- 页面和 API **同源**（都走 :8090），所以没有跨域问题，也不需要在 gopeed 上改 CORS。
- wasm 里只能做 HTTP 客户端（`net.Listen` 在 js/wasm 下是空实现），所以 UI 直接消费 gopeed 的 REST。
- 服务器地址与 API Token 存在浏览器 `localStorage`，默认取页面自身的 origin。

## 功能

| 区域 | 内容 |
| --- | --- |
| 顶栏 | 连接状态圆点（已连接 / 连接失败）、总速度、任务计数 |
| 工具条 | ＋新建任务、全部 / 下载中 / 已完成 分段筛选、全部暂停、全部继续、刷新、搜索框、设置 |
| 任务卡 | 协议徽章（HTTP / BT）、名称、状态、进度条、已下/总大小、↓ 速度、↑ 速度、节点数、连接数、保存路径、详情、暂停/继续、删除 |
| 任务详情 | 点「详情」弹出，见下节；BT / HTTP 各显示自己那几项 |
| 新建下载 | 多行链接批量添加、保存路径、解析后可勾选文件 |
| 设置 | 7 个分页，覆盖 gopeed 全部配置项，见下节 |
| 删除 | 弹确认框，可勾选「同时删除已下载的本地文件」 |
| 登录 | 面板可设访问密码，`/api/*` 由 Caddy 校验；推送接口不受影响 |

数据每 1.5 秒自动刷新；只在任务处于 `running` / `wait` 时才补拉 `/stats`，避免无谓请求。

支持的协议：`http/https`、`bt`（磁力链 / `.torrent` URL / base64）。ed2k 已从本机二进制里移除。
`ftp` / `sftp` / `thunder` 会被服务端拒绝（`unsupported protocol`）。

## 任务详情

任务卡右侧的「详情」按钮会打开一个弹窗，数据来自 `GET /api/v1/tasks/{id}`（完整
meta，列表接口不带 `createdAt`）和 `GET /api/v1/tasks/{id}/stats`（实时数字）：

| 协议 | 额外显示的字段 |
| --- | --- |
| 全部 | 状态、进度（已下/总大小 + 百分比）、大小、已下载、↓/↑ 速度、保存路径、创建时间、任务 ID、来源 |
| `bt` | InfoHash（40 位）、节点（在线/总）、分享率 |
| `http` | 是否支持断点续传、连接数、完整下载地址 |

「来源」对 BT 只显示「种子文件（内嵌 base64）」——`meta.req.url` 是几 KB 的
data URI，原样铺出来没法看。文件列表超过 200 条只列前 200 条。

弹窗里有「刷新」按钮重新取一次；关闭后列表照常每 1.5 秒刷新。

实现上有两个坑，改这块代码时注意：

1. **不能在主线程同步发 HTTP**（wasm 里会把事件循环卡死），必须放到 goroutine；
2. `ui.Modal` 打开时它跑自己的嵌套循环，**别的 goroutine 投给 `RunOnMain` 的函数
   不会被处理**。所以流程是「先在后台取数 → 回到主线程 → 才 `showDetail = true`」，
   弹窗开着期间 `poll()` 也跳过刷新，否则会把回调挂住。

## 设置项（对齐 gopeed 1.9.3 的 `DownloaderStoreConfig`）

| 分页 | 字段 | 对应配置键 |
| --- | --- | --- |
| 常规 | 默认下载目录 | `downloadDir` |
| 常规 | 最大并发任务数 | `maxRunning` |
| 常规 | 下载完 .torrent 自动建 BT 任务 / 建完删种子 | `autoTorrent.enable` / `autoTorrent.deleteAfterDownload` |
| 常规 | 下载完自动解压 / 解压后删压缩包 | `archive.autoExtract` / `archive.deleteAfterExtract` |
| 常规 | 解压密码表（一行一个，从上往下试） | `archive.passwords` |
| 常规 | 自动删除文件已丢失的任务 | `autoDeleteMissingFileTasks` |
| HTTP | 单任务连接数 | `protocolConfig.http.connections` |
| HTTP | User-Agent | `protocolConfig.http.userAgent` |
| HTTP | 使用服务器返回的文件时间 | `protocolConfig.http.useServerCtime` |
| BT | 监听端口 | `protocolConfig.bt.listenPort` |
| BT | 做种到分享率 | `protocolConfig.bt.seedRatio` |
| BT | 做种时长（秒） | `protocolConfig.bt.seedTime` |
| BT | 一直做种不自动停 | `protocolConfig.bt.seedKeep` |
| BT | Tracker 列表（一行一个） | `protocolConfig.bt.trackers` |
| BT | 做种自动清理任务 | `seedAutoRemove.enable`（本机补丁字段） |
| BT | 目标分享率 | `seedAutoRemove.ratio`，0 = 不按分享率判断 |
| BT | 无上传清理（分钟） | `seedAutoRemove.idleMinutes`，0 = 不看空闲；后台每 10 分钟扫一次 |
| BT | 重启后自动续传 | `resumeOnStart`（默认 true） |
| HTTP | 全部下载经代理 | `proxy.mode=all`（本机补丁字段，旧配置看 `proxy.enable`） |
| HTTP | 部分下载经代理 + 关键字列表 | `proxy.mode=partial` + `proxy.keywords[]`（一行一个，网址子串匹配，不区分大小写） |
| HTTP | 用系统环境变量里的代理 / 类型 / 地址 / 用户名 / 密码 | `proxy.system` / `scheme` / `host` / `usr` / `pwd` |
| 通知 | Webhook 开关 + 地址列表 | `webhook.enable` / `urls` |
| 通知 | 脚本开关 + 路径列表 | `script.enable` / `paths` |
| 连接 | 服务器地址、API Token | 只存浏览器 `localStorage`，不上传服务器 |

**关键实现约束**：`PUT /api/v1/config` 是**整份替换**，不是按字段合并
（`pkg/download/downloader.go:952` 直接把入参赋给 `d.cfg.DownloaderStoreConfig`）。
所以保存时 `collectConfig()` 必须把所有字段填满，漏一个就会把它清成零值；
`extra` 这类界面上不展示的字段也要读出来原样带回去。
曾因为只提交 4 个字段，把 `protocolConfig` / `proxy` / `webhook` / `script` 整段清空。

另外 `Task.Name` 不受 `PATCH /tasks/{id}` 影响（那只改 req/opts），改名只能在建任务时给 `opts.name`。

## 目录结构

```
D:\DSH\_mygo\gopeed-web\
├── main.go        状态机、视图（header/toolbar/body/row）、弹窗、轮询
├── client.go      gopeed REST 客户端（同构结构体 + 请求封装）
├── boot.go        js/wasm 专用：隐藏加载遮罩、localStorage、页面 origin
├── web/
│   ├── index.html 装载页（含 loading 遮罩、wasm 加载兜底）
│   ├── wasm_exec.js  Go 官方 wasm 运行时胶水
│   ├── app.wasm    编译产物
│   └── *.gz        预先 gzip 好的副本，Caddy 直接发（零三上现压太慢）
└── app.wasm
```

## 构建

必须用 **pwsh**（bash 下 Go 会因命名管道受限失败），并且重设 `TMP`/`TEMP`：

```powershell
cd D:\DSH\_mygo\gopeed-web
$env:Path = "D:\DSH\.tools\go1.27.3\go\bin;" + $env:Path
$env:GOPATH = "D:\DSH\_work\gopath"; $env:GOCACHE = "D:\DSH\_work\gocache"
$env:GOPROXY = "https://goproxy.cn,direct"; $env:GOSUMDB = "off"; $env:GOFLAGS = "-mod=mod"
$env:TMP = "D:\DSH\_work\tmp"; $env:TEMP = "D:\DSH\_work\tmp"
$env:GOOS = "js"; $env:GOARCH = "wasm"

go vet ./...
go build -o app.wasm .
Copy-Item app.wasm web\app.wasm -Force
```

改完界面记得把 `web/index.html` 里两处 `app.wasm?v=...` 的版本号加一：Caddy 虽然给
`/app.wasm` 发了 `Cache-Control: no-cache`，但 wasm 有 20 MB，换一个 URL 最省心。
标题旁边的 `v2` 是构建标记，同样用来确认浏览器加载到了新产物（改一次加一）。

预压缩（`file_server { precompressed gzip }` 需要同名 `.gz`）：

```powershell
cd web
python -c "import gzip,os
for n in ['app.wasm','index.html','wasm_exec.js']:
    d=open(n,'rb').read()
    g=gzip.open(n+'.gz','wb',compresslevel=9); g.write(d); g.close()
    print(n, len(d), '->', os.path.getsize(n+'.gz'))"
```

## 部署

```bash
# 1) 静态文件
mkdir -p /srv/gopeed-web
# 把 web/ 下 6 个文件（app.wasm、*.gz、index.html、wasm_exec.js）传进去

# 2) Caddy 站点（追加到 /etc/caddy/Caddyfile）
#    见本仓库 caddy-gopeed.conf
caddy fmt --overwrite /etc/caddy/Caddyfile
caddy validate --config /etc/caddy/Caddyfile
systemctl reload caddy
```

Caddy 配置要点：

- `:8090` 纯 HTTP，`encode zstd gzip`，`application/wasm` 由系统 `/etc/mime.types` 提供（已含）。
- `handle /api/*` 反代到 `127.0.0.1:9999`。
- `file_server { precompressed gzip }` 直接发 `.gz`。
- `@wasm` 加 `Cache-Control: no-cache`，避免浏览器拿旧 wasm。

## 删除任务

点任务卡上的「删除」不会再直接删，而是弹一个确认框：

- **不勾选**「同时删除已下载的本地文件」→ 只删任务记录，磁盘上的文件保留（`DELETE /tasks/{id}`）。
- **勾选** → 连文件一起删（`DELETE /tasks/{id}?force=true`），勾上后文案变红、按钮变红并改成「删除任务和文件」。

「下载后重命名」现在由 gopeed 服务端自己完成（详见 `_mygo/gopeed-patched/README.md`）：
改名成功后它会把任务记录里的名字一起改掉，所以 `force=true` 删到的就是改名后那份文件，
**面板不需要再额外补删一次**（以前要调 `gopeed-push` 的 `/cleanup`，那个网关已经拆了）。

## 面板登录

`/api/*` 由 Caddy 校验请求头 `X-Panel-Key`，值 = `base64(用户名:密码)`。
没带或不对就 401，面板会自动切到登录页（顶栏显示「需要登录」）。

当前口令：**`gopeed` / `<your-password>`**

改密码：

```bash
printf 'gopeed:新密码' | base64 | tr -d '\n'     # 得到新的密钥
# 把 /etc/caddy/Caddyfile 里 :8090 站点的 X-Panel-Key 换成新值
caddy validate --config /etc/caddy/Caddyfile && systemctl reload caddy
```

注意两点：

- 用自定义头而不是 HTTP Basic，是因为 Basic 的 401 会带 `WWW-Authenticate`，
  浏览器会弹它自己的原生登录框，和面板的登录页打架。
- 登录状态存在 `localStorage["gopeed.panelkey"]`。改了密码后老浏览器里存的还是旧值，
  在登录页重新输一次即可（或清掉这条 localStorage）。

**`/api/*` 只认这两把钥匙，没有别的免登录接口。** 以前有一套给 anime-go 用的推送接口
（`/push`、`/status`、`/jobs`、`/cleanup`，走 8091 端口的 `gopeed-push` 网关），已经拆掉了：
改名与路径映射都做进了 gopeed 本体，anime-go 现在直连 `/api/v1/tasks`，用 `X-Api-Token` 那把钥匙。

## 自动解压与密码表

在「设置 → 常规」里打开「下载完自动解压压缩包」后，下面有一个「解压密码」文本框，
**一行一个**，遇到加密压缩包时**从上往下逐个试**，第一个能解开的就用它。

```
mikan2024
anime-go
```

优先级：

1. 这条任务自己带的密码（推送接口的 `password` 参数，落到 gopeed 的 `opts.extra.archivePassword`）
2. 面板里的密码表 `archive.passwords`，按顺序

实现上没有用「猜错就重来」的做法，而是**先只读探测**：每个候选密码只读压缩包头部几百字节
验证能否解密，命中后才真正解压一次。所以密码表再长也不会把解压目录写乱。

支持的加密类型：

| 格式 | 是否支持 | 说明 |
| --- | --- | --- |
| zip（ZipCrypto） | ✅ | 传统 zip 加密 |
| zip（AES-256） | ✅ | 7-Zip / WinRAR 默认的强加密 zip |
| rar / 7z / tar.* | ✅ | 由 `mholt/archives` 处理，密码原样传下去 |
| 未加密的压缩包 | ✅ | 即使密码表非空也照常解压（走原来的路径，不受影响） |

密码表里一个都不对时，任务状态正常（`done`），但 `progress.extractStatus` 是 `error`，
日志里会写「密码表里没有能解开这个压缩包的密码（共试了 N 个）」。**不会留下半成品文件，压缩包本身也保留**。

> 注意：这里改的是 gopeed 服务端源码（`pkg/download/extract_password.go`），
> 不是面板自己解压 —— 所以从推送接口加的任务一样享受密码表。

## 给 anime-go 之类程序用的接口

以前这里是个独立的 `gopeed-push` 网关（8091 端口），因为 gopeed 缺三件事：
下载完成后改名、按映射表改写目录、删任务时删到改名后的文件。这三件事现在都做进了
gopeed 本体（补丁与说明见 `_mygo/gopeed-patched/README.md`），网关已拆除，**8091 端口不再监听**。

调用方直接打 gopeed 的 REST 就行，认证用 `X-Api-Token: <your-gopeed-api-token>`：

### 添加下载

```bash
curl -X POST http://127.0.0.1:9999/api/v1/tasks   -H 'X-Api-Token: <your-gopeed-api-token>'   -H 'Content-Type: application/json'   -d '{
        "req":  {"url": "magnet:?xt=urn:btih:..."},
        "opts": {"path": "/media/新番追更/葬送的芙莉莲/Season 1",
                 "rename": "[Airota] Sousou no Frieren - 01 [1080p]"}
      }'
```

`opts` 的字段：

| 字段 | 说明 |
| --- | --- |
| `path` | 保存目录，可以是调用方视角的路径（gopeed 自己按 `pathMappings` 换成本机真实路径） |
| `rename` | 下载完成后的名字，规则见下 |
| `extra.archivePassword` | 这个压缩包专用的解压密码，优先级高于面板里的密码表 |

`rename` 的几条规则（由 gopeed 在本体里执行，规则与老网关一致）：

- 只能是**纯文件名**，空、`.`、`..`、带 `/` 或 `\` 都会被拒绝（只写警告日志，不影响下载本身）。
- 单文件任务如果给的名字**没有后缀**，会自动沿用原来的后缀：给 `episode-02` 下 `Contents-amd64.gz`，
  落盘是 `episode-02.gz`。
- 文件夹任务（BT 多文件）改的是**文件夹名**，里面的文件不动。
- 目标名已存在时**不覆盖**，放弃改名并写警告日志。

改名成功后 gopeed 会把任务记录里的名字一起改掉，所以任务列表显示新名字，
`DELETE /api/v1/tasks/{id}?force=true` 删到的就是改名后那份文件。

### 删除

```bash
curl -X DELETE "http://127.0.0.1:9999/api/v1/tasks/<任务ID>?force=true"   -H 'X-Api-Token: <your-gopeed-api-token>'
```

不再需要额外的 `/cleanup`：改名已经把任务记录路径带上新名字了。

### 路径映射

在 gopeed 配置里（`PUT /api/v1/config`，**整包替换**）：

```json
"pathMappings": [
  {"from": "/media",     "to": "/mnt/nas/media"},
  {"from": "/downloads", "to": "/mnt/nas/dl"}
]
```

按 `path.Clean` 之后的最长 `from` 前缀匹配，剩下的路径原样拼到 `to` 后面；没命中就原样使用。

## 已知限制

| 限制 | 说明 |
| --- | --- |
| 首次加载约 5 MB | wasm 压缩后仍有 5.2 MB，局域网 1～2 秒。后续走缓存 |
| 内存 | Orange Pi Zero 3 只有 969 MB，`gopeed.service` 有 `MemoryMax=400M`。同时下大 BT 任务容易触发 OOM（实测 756 MB 的 Debian ISO 跑到 59 MB 时被 OOM killer 杀过一次，`Restart=on-failure` 自动拉起） |
| 浏览器不支持 | 原生菜单栏、托盘、系统通知、全局快捷键、文件对话框（这些是桌面版能力） |
| 不能改任务名 | gopeed 的 `PATCH /tasks/{id}` 不改 `Task.Name`，UI 里没做改名 |
| 重启后是否续传 | 原生 gopeed 启动时把非 `done`/`error` 的任务置为 `pause`，要手动点继续；本仓库的 gopeed 补丁加了 `resumeOnStart`（默认开），在面板 BT 页可关 |
| 进度分母 | 用 `meta.res.size`；`progress.used` 是**耗时纳秒**不是字节数，不能拿来算百分比 |

## gopeed API 的坑（编码时踩过的）

1. `GET /api/v1/tasks` 的 `data` 是**数组**，不是分页结构。
2. `opts.name` 会**替换落盘文件名**，破坏 `.torrent` 后缀判定（autoTorrent）和压缩包后缀判定（autoExtract）。UI 故意不提供改名。
3. HTTP 单文件资源的 `res.name` / `res.size` 常为空，真值在 `res.files[0]`。
4. `PUT /tasks/{id}/pause` 对已暂停的任务返回 `{"code":1000,"msg":"task not found"}` —— 这是「无需操作」，不是错误，客户端已吞掉。
5. `POST /resolve` 返回的 `rid` **只能用一次**。
6. 磁力链 `resolve` 可能超过 30 秒，客户端对 resolve 用 120 秒超时的连接。
7. `DELETE /tasks/{id}?force=true` 才会连文件一起删。
8. BT 任务 force 删除偶发 nil pointer panic（`internal/protocol/bt/fetcher.go:146`），任务仍会被删掉。

## MyGo 的坑（编码时踩过的）

MyGo 是立即模式 UI（Go → WebAssembly，canvas 绘制），有两处行为会直接吃掉功能，改界面前先看这里。

**1. `ui.Switch` 的旋钮位置来自动画通道，跨重建会复用**

`ui.Switch` 内部是 `pos := sw.Animate("knob", b2f(*v), 140*time.Millisecond)`，动画通道挂在按元素 id 复用的 node state 上。设置弹窗每次重建，同一个开关会拿到上一帧残留的通道（值 0），于是**绑定变量已经是 true，旋钮还停在左边、轨道还是灰的**——服务端配置开着、界面显示关，就是这个原因。DEBUG 打印能证明字段值是对的，所以很容易查错方向。

本面板不再用 `ui.Switch`，改成自己画的 `switchBox`：`Draw` 回调每帧按 `*v` 现算旋钮位置和轨道色，不参与任何动画。

**2. Modal 打开后，别的 goroutine 投回主线程的函数不会被处理**

`ui.Modal` 打开时 MyGo 跑的是它自己的嵌套事件循环，`mygo.RunOnMain` / `Window.Update` 投递的闭包在此期间不会被执行（wasm 单线程，新起的 goroutine 也没有调度机会）。所以「先弹窗、再异步取配置」在 wasm 里必然卡死——弹窗永远显示「正在读取服务器配置…」、保存按钮灰着「处理中…」，点保存也没反应。

本面板的约束是：**弹窗必须在数据就绪后同步打开**。设置项在启动（`primeConfig`）和每次保存成功后都已经读进内存，`openSettings()` 只负责置标志位；保存时先关弹窗、再异步 `PUT`，成功后回填并提示。

顺带一提，goroutine 改界面状态统一用非阻塞的 `a.post(fn)`（内部 `Window.Update`），不要用会阻塞等返回的 `mygo.RunOnMain`。

**3. 后台标签页不重绘**

`internal/web/surface.go` 的 `RequestFrame()` 用 `requestAnimationFrame` 排帧，标签页在后台时浏览器不给回调，`pending` 卡在 true，之后所有重绘请求都被丢弃——表现为「切回来画面还是旧的，随便点一下就全好了」。调试时先 `tab_focus` 或开个新标签页，别把它当成代码 bug。

**4. 构建不要加 `-trimpath`**

加了之后面板会永久停在「正在加载…」（wasm 里的路径信息影响启动阶段的资源解析）。`GOOS=js GOARCH=wasm go build -o app.wasm .` 即可。

## 验证记录

在真实浏览器（Edge 157，headless + CDP）里逐项点过：

- 页面加载：canvas 1366×803，无 JS 异常，加载遮罩正常消失
- 空态、任务卡渲染、进度条、速度、节点数、路径
- ＋新建任务 → 填链接 + 路径 → 开始下载 → 卡片出现 → 完成，toast「已添加 1 个任务」
- 设置弹窗回填（服务器地址 `http://192.0.2.10:8090`、最大并发 5、两个开关）
- 搜索过滤（无匹配时显示「没有符合条件的任务」）
- 分段筛选、全部暂停 / 全部继续、删除
- HTTP / BT 两种任务都能建，`ed2k://` 与 `ftp://` 正确报 `unsupported protocol`

部署后清理：远端任务数 0，`downloadDir=''`、`maxRunning=5`、`autoTorrent=false`、`autoExtract=false`，`/tmp` 无残留。
