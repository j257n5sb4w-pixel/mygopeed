# gopeed-push（已停用，仅存档）

**这个网关已经不在一线使用了。** 它的三件事——下载完成后改名、删除时清理改名后的文件、目录映射——
现在已经做进 gopeed 本体（见 `..\gopeed-patched\`），服务器上 `gopeed-push.service`、`/opt/gopeed-push`、
`/var/lib/gopeed-push` 都已删除，**8091 端口已释放**。

留下这份源码只为两个用途：

1. 想知道当初 `opts.rename` 的规则是怎么定的（`sanitizeName` / `doRename` / 后缀沿用 / 目标已存在不覆盖），
   这里是最早的实现，gopeed 里的 `cleanRenameName` 和 `renameResult` 就是照它写的。
2. 万一哪天要把网关再拉起来做别的事。

## 当初的接口（现在都打不通了）

```
GET  /push?url=<链接>&dir=<保存目录>&name=<重命名后的名字>
POST /push   {"url":"...","dir":"...","name":"...","password":"..."}
GET  /status?id=<任务ID>
GET  /jobs
POST /cleanup?id=<任务ID>
GET  /health
```

## 现在该怎么做

不要再起这个网关。要给 gopeed 加一个「下完改名」的任务，直接打 gopeed 的 REST：

```bash
curl -s -X POST http://127.0.0.1:9999/api/v1/tasks \
  -H 'X-Api-Token: <token>' \
  -H 'Content-Type: application/json' \
  -d '{"req":{"url":"magnet:?xt=urn:btih:..."},
       "opts":{"path":"/media/新番追更/某番/Season 1",
                "rename":"某番 - 01",
                "selectFiles":[]}}'
```

- `path` 会先经过 gopeed 配置里的 `pathMappings` 换成本机真实路径；
- `rename` 是纯文件名，下载完成后 gopeed 自己改名，单文件没后缀时沿用原后缀，多文件改的是文件夹名，目标已存在不覆盖；
- 改名之后任务记录里的名字跟着变，所以 `DELETE /api/v1/tasks/{id}?force=true` 删到的就是改名后那份文件，
  不再需要 `/cleanup`。

细节：`..\gopeed-patched\README.md`、`..\gopeed-web\README.md`。

## 文件

- `main.go` —— 网关源码（778 行，原样保留）
- `gopeed-push-linux-arm64` —— 当时部署到 `/opt/gopeed-push/gopeed-push` 的产物（6.7MB）
- `go.mod`
