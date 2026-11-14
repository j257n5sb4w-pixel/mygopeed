# gopeed-patched — 本机在用的 gopeed 1.9.3 补丁版

服务器 `192.0.2.10` 上的 `/opt/gopeed/bin/gopeed` **不是官方原版**，是 `gd/` 目录源码编译出来的 headless 版
（`go build ./cmd/gopeed-headless -tags nosqlite`，只开 REST、不嵌前端，`/` 返回 404，和官方 headless 行为一致）。

这份源码在官方 1.9.3 基础上叠了 **9 类改动**（表格按「能被删的都删了、缺的能力都补上」排列）：

| 改动 | 文件 | 说明 |
| --- | --- | --- |
| 解压密码表 | `pkg/base/model.go`、`pkg/download/extract_password.go`、`pkg/download/downloader.go` | `archive.passwords` 全局候选密码，按顺序试；任务自带 `opts.extra.archivePassword` 优先 |
| 解压后延迟删除 | `pkg/download/downloader.go` | 全局 `archive.deleteAfterExtract` 真正生效 + 解压完成 5 秒后才删压缩包 |
| **下载完成后自动改名**（新增） | `pkg/base/model.go`、`pkg/download/downloader.go`、`internal/protocol/bt/fetcher.go` | 建任务时给 `opts.rename`，任务 done 后 gopeed 自己 `os.Rename`，并同步任务记录里的名字 |
| **路径映射**（新增） | `pkg/base/model.go`、`pkg/download/downloader.go` | 配置项 `pathMappings: [{from,to}]`，建任务时把 `opts.path` 按最长前缀换成本机真实路径 |
| **BT 删除空指针保护**（新增） | `internal/protocol/bt/fetcher.go` | `Close()` 检查 `client != nil` 再取 `Torrents()`，避免删任务时 panic |
| **做种自动清理**（2026-11-14 合并） | `pkg/base/model.go`、`pkg/download/seed_autoremove.go` | `seedAutoRemove{enable,ratio,idleMinutes}`：BT 任务 done 后分享率达标、或连续 N 分钟没有上传字节，就删掉任务记录（`force=false`，磁盘文件保留），后台每 10 分钟扫一次 |
| **启动后自动续传**（2026-11-14 合并） | `pkg/base/model.go`、`pkg/download/resume_on_start.go`、`pkg/download/downloader.go` | `resumeOnStart`（默认 true）：重启后把原本 running/wait 的任务自动继续；手动暂停的不动 |
| **代理只管 HTTP**（2026-11-14） | `pkg/base/model.go`、`pkg/download/downloader.go` | `proxy.mode` = `off`/`all`/`partial` + `proxy.keywords[]`；只有 http 协议拿到代理函数，BT 永远直连；partial 模式按请求 URL 子串匹配，命中才走代理 |
| **移除 ed2k 协议**（2026-11-14） | 删除 `internal/protocol/ed2k/`、`pkg/protocol/ed2k/`；`pkg/download/model.go`、`go.mod` | ed2k 全链路删掉（FetcherManager 只留 http/bt、goed2k 依赖移除），`ed2k://` 链接统一报 `unsupported protocol` |

完整 diff 一次打全：**`local-patches.patch`**（共 **17 个文件**：8 个改动、5 个新增、4 个删除（ed2k 两个目录下的 4 个文件），2006 行；`patch -p1` 可直接打，已 dry-run + 实打后与源码树逐字节比对验证）。

> **2026-11-14 合并说明**：另一个 agent 曾用「官方 1.9.3 + 做种清理」重编了一版覆盖线上，把这里的改名 / 路径映射 / 密码表 / 延迟删包全丢了。现在两边已经合并回一份源码树（就是本目录 `gd/`），线上二进制 `1.9.3-local` 同时带全部七类改动，编译命令见下。

```bash
# 合并版（线上就是它）
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags "nosqlite,web" -trimpath \n  -ldflags "-s -w -X github.com/GopeedLab/gopeed/pkg/base.Version=1.9.3-local" -o gopeed ./cmd/web
```
旧的 `delete-after-extract.patch` 只含解压那一半，留着当分项参考。

## 为什么需要这些补丁

### 解压后删除

官方 v1.9.3 有两个问题，导致面板里「解压后删除」形同虚设：

1. **全局开关没人读**：`doOnDone` 只读了 `archive.autoExtract`，`archive.deleteAfterExtract` 写进配置后没有任何代码使用。
2. **就算生效也是立刻删**：`handleExtractionResult` 解压一结束就 `os.Remove`，没有延迟；刚解压完的大文件还在被索引/播放时删源包容易互相干扰。

### 自动改名 + 删除删到改名后文件

原来 gopeed 三件事都缺，只能在外面挂一个 8091 的「推送网关」补：

1. 没有「下载完成后改成我想要的名字」的能力（`opts.name` 是**加任务时**就改名，会破坏 `autoTorrent`/`autoExtract` 的后缀判断，且 BT 任务实际落盘目录名来自种子元数据，改不动）；
2. 删除任务时按 `meta` 里的原始路径删，文件改过名就删不到，所以网关另外提供了一个 `/cleanup`；
3. 没有「调用方视角的目录 → 本机真实目录」的映射表，网关只能把调用方给的绝对路径原样透传。

三样现在都进了 gopeed 本体，网关（`gopeed-push`，8091 端口）已经拆掉。

## 补了什么

### 1. 让全局解压删除开关生效（`doOnDone`）

```go
// Determine if the archive should be deleted after extraction.
// The per-task flag wins; otherwise fall back to the global config.
// (Upstream only reads Archive.AutoExtract here, so the global
// archive.deleteAfterExtract switch had no effect at all.)
if !e.DeleteAfterExtract && cfg != nil && cfg.Archive != nil {
    e.DeleteAfterExtract = cfg.Archive.DeleteAfterExtract
}
```

任务级 `opts.extra.deleteAfterExtract` 优先，没设就跟随全局。

### 2. 删除延后 5 秒、放到独立 goroutine（`handleExtractionResult`）

```go
// archiveDeleteDelay is how long an archive stays on disk after a successful extraction
const archiveDeleteDelay = 5 * time.Second
```

```go
if deleteAfterExtract {
    files := append([]string(nil), archiveFiles...)
    go func() {
        time.Sleep(archiveDeleteDelay)
        for _, archiveFile := range files {
            deleteErr := os.Remove(archiveFile)
            ...
        }
    }()
}
```

- 解压成功 → 先把任务置成 `extractStatus=done`，5 秒后再删压缩包（多卷逐个删）。
- 删除在 goroutine 里做，不占 gopeed 那条**单线程**解压队列（`pkg/download/extract_queue.go` 是串行 worker）。
- 日志：`archive deleted after extraction: <path>` / `delete archive after extraction failed: <path>`，写在 `/opt/gopeed/storage/logs/core.log`，**不在 journald**。

### 3. 下载完成后自动改名（`opts.rename`）

`pkg/base/model.go` 的 `Options` 加了字段：

```go
// Rename is the name the download result gets once the download is done.
// It is a bare file name (no directory separator). For a single file task the
// original extension is kept when Rename has none; for a multi file task the
// torrent folder is renamed. After a successful rename Name is updated to the
// new base name, so task display and "delete with files" follow the new name.
Rename string `json:"rename"`
```

`pkg/download/downloader.go` 的 `watch()` 里，任务转 done、`storage.Put` 之后立刻 `d.applyRename(task)`，
**接在所有后处理（autoTorrent / autoExtract）读磁盘路径之前**，这样它们看到的都是改名后的路径。

`applyRename` / `renameResult` / `cleanRenameName` 的规则（和老网关一致）：

- 只接受裸文件名：空、" . "、" .. "、含 `/` 或 `\` 都直接放弃（只 WARN，不影响下载本身）。
- 单文件任务、新名字没有扩展名 → 沿用原文件扩展名。
- 多文件（BT 种子带文件夹）→ 改的是那个文件夹名。
- 目标已存在 → 报错放弃，**不覆盖**。
- 改名成功 → 把 `task.Meta.Opts.Name`（以及 `Res.Name` 或 `Files[0].Name`）一起改成新名字，于是：
  - 任务列表显示新名字；
  - `DELETE /api/v1/tasks/{id}?force=true` 按新名字删，**删到的就是改名后那份文件**，不再需要 `/cleanup`。

BT 任务做种时会盯着原路径，文件一改名它以为数据丢了，会按旧名重建 `.part`，极端情况下 anacrolix 会 panic。
所以 `internal/protocol/bt/fetcher.go` 加了 `DetachForRename()`：`os.Rename` 之前先把这条种子从 client 摘掉（`safeDrop` + `torrentDropFunc` + 非阻塞通知 `uploadDoneCh`），
并把任务标记成 `Uploading=false`，重启也不会恢复上传。`Close()` 里那次 `uploadDoneCh <- nil` 也改成了非阻塞 select（cap=1，重复关闭原来会永久阻塞）。
`Close()` 结尾原来直接 `len(client.Torrents())`，上一个任务把 client 关掉（`client = nil`）之后再删任务就会空指针 panic
（日志里 `panic: DELETE /api/v1/tasks/...` 出现过多回），现在加了 `client != nil &&` 守卫。

### 4. 路径映射（`pathMappings`）

`pkg/base/model.go` 的 `DownloaderStoreConfig` 加了：

```go
type PathMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}
```

`initOptions()` 里在 `util.ReplacePathPlaceholders` 之后、`WhiteDownloadDirs` 白名单检查之前，把 `opts.Path` 过一遍 `MapPath`：

```go
if storeConfig, err := d.GetConfig(); err == nil {
	opts.Path = storeConfig.MapPath(opts.Path)
}
```

`MapPath` 按 `path.Clean` 之后的最长 `From` 前缀匹配，剩下的路径原样拼到 `To` 后面；没命中就原样返回。
典型配置（本机）：

```json
"pathMappings": [
  {"from": "/media",     "to": "/mnt/nas/media"},
  {"from": "/downloads", "to": "/mnt/nas/dl"}
]
```

于是调用方（anime-go、面板、插件）可以一直用自己那套挂载路径 `/media/新番追更/...`，gopeed 这边落到 `/mnt/nas/media/新番追更/...`。

## 构建（Windows 交叉编译到 zero3 的 arm64）

```bash
cd gd
export TMP=/d/DSH/_work/tmp TEMP=/d/DSH/_work/tmp TMPDIR=/d/DSH/_work/tmp \
       GOCACHE=/d/DSH/_work/gocache GOPATH=/d/DSH/_work/gopath \
       GOFLAGS="-mod=mod -buildvcs=false" GOPROXY=https://goproxy.cn,direct \
       GOOS=linux GOARCH=arm64 CGO_ENABLED=0
/d/DSH/.tools/go1.27.3/go/bin/go.exe build -tags "nosqlite" -trimpath \
  -ldflags "-s -w -X github.com/GopeedLab/gopeed/pkg/base.Version=1.9.3-headless" \
  -o /d/DSH/_work/gopeed-linux-arm64 ./cmd/gopeed-headless
```

要点：

- **必须带 `GOOS=linux GOARCH=arm64 CGO_ENABLED=0`**。漏了会产出 Windows PE，上传后 systemd 报 `status=203/EXEC`（Exec format error）——已经踩过一次。
- 一定要编 `./cmd/gopeed-headless`（本仓库自带的无 UI 入口，`WebEnable: false`）。`./cmd/web` 需要 `//go:embed dist/*` 里的 Flutter 产物，而且会挂上 web 路由。
- 产物约 38.5 MB（`-trimpath -s -w`），和官方 headless 版大小相仿。
- 源码里的 `ui/` 目录已删掉（构建 headless 用不到，能省 2 MB+）；要编 `cmd/web` 得先把 Flutter 前端产物放回去。

## 部署

```bash
# 上传后
systemctl stop gopeed
cp -a /opt/gopeed/bin/gopeed /opt/gopeed/bin/gopeed.bak.$(date +%Y%m%d-%H%M%S)
install -m 0755 -o root -g root /root/gopeed-new /opt/gopeed/bin/gopeed
systemctl start gopeed
curl -s http://127.0.0.1:9999/api/v1/info        # {"version":"1.9.3-headless",...}
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:9999/   # 404
```

bolt 数据库（`/opt/gopeed/storage/gopeed.db`）与配置文件向后兼容，换 binary 不丢任务；`/opt/gopeed/bin/gopeed.bak.*` 留着回滚。

配置（`pathMappings` 等）用 `PUT /api/v1/config` 整包写入，注意是**整包替换**，改之前先 `GET` 一份。

## 验证记录

1. 全局 `archive.deleteAfterExtract=true`，推一个 zip：日志 `auto extract archive completed` 后正好 5 秒出现 `archive deleted after extraction`，解压出的文件保留。
2. 把全局开关改成 `false` 再推一次：12 秒后压缩包仍在（开关确实生效，不是无脑删）；之后改回 `true`。
3. `GET /api/v1/config` 里 `archive.passwords` 仍在（`["loivip.com"]`），密码表功能没被破坏。
4. **改名**：`POST /api/v1/tasks` 带 `opts:{path:"/media/_t", rename:"剧集 第01话"}`，落盘为 `/mnt/nas/media/_t/剧集 第01话.html`
   （`/media` → `/mnt/nas/media` 的映射生效，`.html` 后缀自动沿用，任务记录 `name` 同步成 `剧集 第01话.html`）。
5. **删到改名后文件**：同一任务 `DELETE /api/v1/tasks/{id}?force=true` 后，`/mnt/nas/media/_t` 目录里文件消失。
6. **BT 单文件**：自造只有 webseed（BEP19 url-list）的种子，`opts:{path:"/media/_b", rename:"BT 测试 第01话"}` →
   落盘 `/mnt/nas/media/_b/BT 测试 第01话.bin`（`.bin` 后缀沿用），`uploading=false`，20 秒后旧名 `.part` 没有回来，
   `force` 删除后目录清空。这条同时验证了 `DetachForRename`：不先摘种子就直接改名，anacrolix 会按旧名重建 `.part`。
7. **BT 多文件**：同样自造种子（3 个文件、含子目录），`rename:"多文件 第02话"` → 磁盘上是
   `多文件 第02话/{a.bin,c.txt,sub/b.bin}`，`meta.res.name` 与新目录名一致，`force` 删除把整个目录删掉。
8. **8091 已释放**：`ss -lntp | grep 8091` 无输出；`systemctl is-active gopeed-push` = `inactive`，单元与 `/opt/gopeed-push`、`/var/lib/gopeed-push` 均已删除；
   Caddyfile 里指向 8091 的 `/push /status /jobs /cleanup /health` 反代块也已整段移除（`caddy validate` 通过）。

## 注意

- **升级官方版本会丢这些改动**。升级前把 `local-patches.patch` 重新打到新源码上（含密码表、延迟删包、改名、路径映射、BT 删除空指针保护、做种自动清理、启动续传、代理分流、移除 ed2k 共九部分）。
- 加密压缩包密码是**按任务**存 `opts.extra.archivePassword`，全局密码表在 `archive.passwords`，两者可以并存，任务级优先。
- 改名是「下载完成之后」做，不是下载前。想下载前就定名得用 `opts.name`，但那会破坏后缀判断，不建议。
- 曾经有个叫 `gopeed-extract-clean` 的外挂 Python 守护进程做解压删除，已彻底删掉（systemd 单元、`/opt/gopeed-extract-clean`、`/var/lib/gopeed-extract-clean` 全部移除）。
- 曾经有个叫 `gopeed-push` 的推送网关（8091）做改名/映射/清理，同样已彻底删掉，功能已并入 gopeed 本体。

## 6. 做种自动清理 `seedAutoRemove`

BT 任务正常下载完成后，满足下面任意一条就把**任务记录**删掉（`force=false`，磁盘文件不动）：

- 分享率（上传 ÷ 下载）≥ `ratio`
- 连续 `idleMinutes` 分钟没有任何新的上传字节（没人拉了）

```json
"seedAutoRemove": { "enable": true, "ratio": 2, "idleMinutes": 30 }
```

- `ratio: 0` / `idleMinutes: 0` 分别表示不按该条件判断；`enable: false` 全关
- 后台**每 10 分钟**扫一次（`seedAutoRemoveInterval`），所以 `idleMinutes` 要明显大于 10 才有意义
- 跳过 `extractStatus` 为 `queued/waitingParts/extracting` 的任务，避免删掉正在解压的记录
- 空闲时长用内存里的「上传字节数多久没涨」判断，进程重启会给每个任务重新计时

## 7. 启动后自动续传 `resumeOnStart`

gopeed 加载任务时会把所有非 done/error 任务统一置为 `pause`（`Setup` 里写死），所以进程重启后下到一半的任务会全部停住。
补丁在同一个循环里先记下原本是 `running`/`wait` 的 ID，2 秒后逐个 `Continue`：

```json
"resumeOnStart": true
```

- 缺省或没有这个键 = 开；`false` 关
- 手动暂停的任务落盘状态就是 `pause`，不在恢复名单里
- 日志：`resume on start: found N unfinished tasks` / `resume on start: 已恢复 N 个未完成任务`

## 8. 代理只作用于 HTTP，并支持按网址关键字分流

原版把 `proxy` 交给每个协议：`bt` 拿它当 tracker/peer 的 HTTP 代理，`http` 拿它当下载代理，
实际用起来 BT 挂代理只会拖慢 tracker 和 peer 发现，所以本补丁把范围收紧：

- `downloader.setupFetcher` 里按 `fm.Name()` 判断，**只有 `http` 拿到代理函数**；`bt` 一律返回 `nil`（直连）。
- 配置加了 `mode` 与 `keywords`：

```json
"proxy": {
  "enable": true,
  "mode": "partial",
  "keywords": ["github.com", "pixiv"],
  "scheme": "http",
  "host": "127.0.0.1:7890",
  "usr": "",
  "pwd": ""
}
```

| `mode` | 行为 |
| --- | --- |
| `off` | 谁都不走代理 |
| `all` | 所有 HTTP / HTTPS 下载走代理 |
| `partial` | 只有请求网址里含 `keywords` 任意一项（子串、不区分大小写）才走代理，其余直连 |

- 兼容老配置：`mode` 为空时按 `enable` 推成 `all` / `off`，`Init()` 会把两者同步写回。
- 判断发生在**每次请求**（`Transport.Proxy` 回调里 `Resolve(r.URL)`），所以改配置、加关键字对正在跑的任务立即生效，不用重建客户端。
- `keywords` 为空 = partial 模式下谁都不走代理（不会误把所有流量塞进代理）。
- 任务级的 `req.proxy`（`follow` / `none` / `custom`）仍然优先，语义不变。
- 扩展安装用的 git clone 只认 `all` 模式（`ToUrl()`），`partial` 时按直连处理。

## 面板对应位置

本仓库的 `gopeed-web/`（MyGo wasm 面板）的设置弹窗 → **BT** 标签页，已内置这两个功能的开关和输入框：

- 「做种自动清理任务」+「目标分享率」+「无上传清理（分钟）」
- 「重启后自动续传」

面板的 `gConfig` 必须声明 `seedAutoRemove` / `resumeOnStart` / `pathMappings` 三个字段，因为 `PUT /api/v1/config`
是整份替换，漏声明就会把它们清空（这条曾经真实发生过一次，见上面合并说明）。
