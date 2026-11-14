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
