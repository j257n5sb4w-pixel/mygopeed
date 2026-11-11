// gopeed-push 是 gopeed 的一个很小的「推送网关」。
//
// 目的：让 anime-go 之类的自动化程序用一条命令把下载丢给 gopeed，
// 并且能指定「下载到哪里」和「下载完成后重命名成什么」。
//
// 为什么不让调用方直接用 gopeed 的 REST：
//  1. gopeed 的 opts.name 是「下载时就用这个名字」，对 BT 多文件任务改的是
//     文件夹名，而且会破坏 autoTorrent / autoExtract 的后缀判断；
//  2. 真正意义的「下载后重命名」需要有人在任务 done 之后去改文件名，
//     gopeed 自己不做这件事，所以由这个小服务盯着任务完成后改名。
//
// 接口（都不需要账号密码；如需保护，设 PUSH_TOKEN 环境变量即可）：
//
//	GET  /push?url=<链接>&dir=<保存目录>&name=<重命名后的名字>
//	POST /push   {"url":"...","dir":"...","name":"...","token":"..."}
//	GET  /status?id=<任务ID>   查询下载与重命名的进展
//	GET  /health               健康检查
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- 配置 ----------

type config struct {
	listen    string
	gopeedURL string
	token     string
	statePath string
	roots     []string
}

func loadConfig() config {
	c := config{
		listen:    envOr("PUSH_LISTEN", "127.0.0.1:8091"),
		gopeedURL: strings.TrimRight(envOr("GOPEED_URL", "http://127.0.0.1:9999"), "/"),
		token:     os.Getenv("PUSH_TOKEN"),
		statePath: envOr("PUSH_STATE", "/var/lib/gopeed-push/jobs.json"),
	}
	for _, s := range strings.Split(envOr("PUSH_ROOTS", "/mnt/nas/dl"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			c.roots = append(c.roots, s)
		}
	}
	return c
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// ---------- gopeed 客户端 ----------

type gResult struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type gFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type gResource struct {
	Name  string  `json:"name"`
	Size  int64   `json:"size"`
	Files []gFile `json:"files"`
	Hash  string  `json:"hash"`
}

type gOpts struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type gTask struct {
	ID       string     `json:"id"`
	Status   string     `json:"status"`
	Protocol string     `json:"protocol"`
	Name     string     `json:"name"`
	Meta     *gMeta     `json:"meta"`
	Progress *gProgress `json:"progress"`
	Error    string     `json:"error"`
}

type gMeta struct {
	Req  json.RawMessage `json:"req"`
	Res  *gResource      `json:"res"`
	Opts *gOpts          `json:"opts"`
}

type gProgress struct {
	Downloaded  int64 `json:"downloaded"`
	Speed       int64 `json:"speed"`
	UploadSpeed int64 `json:"uploadSpeed"`
}

type gClient struct {
	base string
	http *http.Client
}

func newGClient(base string) *gClient {
	return &gClient{base: base, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *gClient) do(method, p string, body any) (*gResult, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, c.base+p, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r gResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("gopeed 返回的不是预期 JSON（HTTP %d）：%s", resp.StatusCode, trunc(string(raw), 200))
	}
	if r.Code != 0 {
		return nil, errors.New(r.Msg)
	}
	return &r, nil
}

func (c *gClient) create(u, dir, password string) (string, error) {
	opts := map[string]any{
		"path":        dir,
		"selectFiles": nil,
	}
	if password != "" {
		// gopeed 的 http fetcher 会把这个 extra 解析成 OptsExtra，
		// archivePassword 是「这条任务自己的解压密码」，优先级高于服务端密码表。
		opts["extra"] = map[string]any{"archivePassword": password}
	}
	body := map[string]any{
		"req":  map[string]any{"url": u},
		"opts": opts,
	}
	r, err := c.do("POST", "/api/v1/tasks", body)
	if err != nil {
		return "", err
	}
	var id string
	if err := json.Unmarshal(r.Data, &id); err != nil {
		return "", fmt.Errorf("解析任务 ID 失败：%v", err)
	}
	return id, nil
}

func (c *gClient) task(id string) (*gTask, error) {
	r, err := c.do("GET", "/api/v1/tasks/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	var t gTask
	if err := json.Unmarshal(r.Data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ---------- 任务登记表 ----------

type job struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Dir       string    `json:"dir"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // pending / done / error / renamed
	Message   string    `json:"message"`
	FinalPath string    `json:"finalPath"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type registry struct {
	mu   sync.Mutex
	jobs map[string]*job
	path string
	cli  *gClient
}

func newRegistry(p string, cli *gClient) *registry {
	r := &registry{jobs: map[string]*job{}, path: p, cli: cli}
	r.load()
	return r
}

func (r *registry) load() {
	if r.path == "" {
		return
	}
	b, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var list []*job
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, j := range list {
		r.jobs[j.ID] = j
	}
}

// save 把登记表落盘，重启后未完成的改名任务不丢。
func (r *registry) save() {
	if r.path == "" {
		return
	}
	list := make([]*job, 0, len(r.jobs))
	for _, j := range r.jobs {
		list = append(list, j)
	}
	sort.Slice(list, func(i, k int) bool { return list[i].CreatedAt.Before(list[k].CreatedAt) })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return
	}
	tmp := r.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, r.path)
	}
}

func (r *registry) add(j *job) {
	r.mu.Lock()
	r.jobs[j.ID] = j
	r.save()
	r.mu.Unlock()
}

func (r *registry) update(id string, fn func(*job)) {
	r.mu.Lock()
	if j, ok := r.jobs[id]; ok {
		fn(j)
		j.UpdatedAt = time.Now()
	}
	r.save()
	r.mu.Unlock()
}

func (r *registry) get(id string) (*job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return nil, false
	}
	cp := *j
	return &cp, true
}

func (r *registry) pending() []*job {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*job
	for _, j := range r.jobs {
		if j.Status == "pending" {
			cp := *j
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAt.Before(out[k].CreatedAt) })
	return out
}

// ---------- 重命名 ----------

// resolvePaths 依据 gopeed 的路径规则算出「下载完的真实路径」。
// 对应 internal/fetcher/fetcher.go 的 FolderPath / SingleFilepath / RootDirPath：
//   - res.name 非空 → 这是一个文件夹任务，根目录是 join(opts.path, res.name)
//   - 否则 → 单文件，路径是 join(opts.path, files[0].path, files[0].name)
func resolvePaths(t *gTask) (root string, single bool, err error) {
	if t.Meta == nil || t.Meta.Res == nil {
		return "", false, errors.New("任务缺少资源信息")
	}
	base := ""
	if t.Meta.Opts != nil {
		base = t.Meta.Opts.Path
	}
	res := t.Meta.Res
	if res.Name != "" {
		return path.Join(base, res.Name), false, nil
	}
	if len(res.Files) == 0 {
		return "", false, errors.New("任务没有文件列表")
	}
	f := res.Files[0]
	return path.Join(base, f.Path, f.Name), true, nil
}

// sanitizeName 校验重命名只给一个纯文件名，避免调用方用 ../ 把文件写到别处。
func sanitizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("重命名不能为空")
	}
	if strings.ContainsAny(name, "/\\") {
		return "", errors.New("重命名只能是文件名，不能带目录分隔符")
	}
	if name == "." || name == ".." {
		return "", errors.New("重命名不合法")
	}
	return name, nil
}

// doRename 在任务完成后把下载结果改成调用方要的名字。
// 单文件任务改文件名；文件夹任务（BT 多文件）改文件夹名。
// 返回改名前的真实路径与改名后的路径，供 guardAfterRename 事后盯着旧路径。
func doRename(t *gTask, want string) (old, dst string, err error) {
	old, single, err := resolvePaths(t)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(old); err != nil {
		return old, "", fmt.Errorf("找不到下载结果 %s：%v", old, err)
	}
	newName, err := sanitizeName(want)
	if err != nil {
		return old, "", err
	}
	dir := filepath.Dir(old)
	dst = filepath.Join(dir, newName)
	if single {
		// 单文件时如果带了后缀就尊重调用方，没带就沿用原后缀
		if filepath.Ext(newName) == "" {
			if e := filepath.Ext(old); e != "" {
				dst = filepath.Join(dir, newName+e)
			}
		}
	}
	if dst == old {
		return old, old, nil
	}
	if _, err := os.Stat(dst); err == nil {
		return old, "", fmt.Errorf("目标 %s 已存在，没有覆盖", dst)
	}
	if err := os.Rename(old, dst); err != nil {
		return old, "", err
	}
	return old, dst, nil
}

// ---------- 后台监视 ----------

func (r *registry) watch(j *job) {
	deadline := time.Now().Add(24 * time.Hour)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		t, err := r.cli.task(j.ID)
		if err != nil {
			// 任务可能刚被删掉，多等几轮再说
			continue
		}
		switch t.Status {
		case "done":
			if j.Name == "" {
				r.update(j.ID, func(x *job) { x.Status = "done"; x.Message = "下载完成" })
				return
			}
			// 先等 gopeed 撒手再改名。它报 done 之后任务并没有结束：还在做种、
			// 校验，文件一改名它就认为数据丢了，按旧名字重新拉一份 .part 出来；
			// 而重下过程中文件被删/被改，会触发 anacrolix 的 panic
			// （peerconn.go servePeerRequest 里 panicif.True(MapContains(...))），
			// 整个 gopeed 崩掉，所有在下的任务全部回落到 pause。
			if !waitSettle(r.cli, j.ID, 180*time.Second) {
				log.Printf("任务 %s 完成后校验迟迟没结束（3 分钟），照常改名，后续留意残留", j.ID)
			}
			if t2, err := r.cli.task(j.ID); err == nil {
				t = t2
			}
			old, dst, err := doRename(t, j.Name)
			if err != nil {
				r.update(j.ID, func(x *job) {
					x.Status = "error"
					x.Message = "重命名失败：" + err.Error()
				})
				log.Printf("rename %s failed: %v", j.ID, err)
				return
			}
			r.update(j.ID, func(x *job) {
				x.Status = "renamed"
				x.FinalPath = dst
				x.Message = "下载完成并已重命名"
			})
			log.Printf("rename %s -> %s", j.ID, dst)
			// 任务留着：gopeed 已经停种，文件不会被动；anime-go 的下载页也还能
			// 看到这条已完成的记录。只盯一眼旧路径有没有被重新拉起来。
			go watchStray(old, dst)
			return
		case "error":
			msg := "任务出错"
			if t.Error != "" {
				msg = "任务出错：" + t.Error
			}
			r.update(j.ID, func(x *job) { x.Status = "error"; x.Message = msg })
			return
		}
	}
	r.update(j.ID, func(x *job) { x.Status = "error"; x.Message = "等待超时（24 小时）" })
}

// waitSettle 等这条任务真正写完：状态 done 且下载速度为 0 连着几秒。
//
// 为什么必须等：gopeed 报 done 之前还在收尾（校验、把 .part 换成正式名），
// 这时候把文件改名会被它当成数据丢失；更糟的是从活着的任务底下抽走文件，
// anacrolix 会在 servePeerRequest 里 panic（peerconn.go 的
// panicif.True(MapContains(c.unreadPeerRequests, r))），整个 gopeed 崩掉，
// 重启后所有没下完的任务变成 pause —— 用户看到的就是「下载到一半自己暂停」。
// 不看上传速度：gopeed 下完还会一直做种（seedRatio 5 / seedTime 0 等于永不
// 停），拿上传当判据会永远等不完；做种期间改名实测不会再下（文件句柄还在）。
func waitSettle(cli *gClient, id string, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	quiet := 0
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		t, err := cli.task(id)
		if err != nil {
			continue
		}
		// 只看「状态 done + 结果已经是正式名字」。gopeed 对已完成任务也会一直报一个
		// 非零 speed（做种中），拿速度当判据会永远等不完。
		if t.Status == "done" && settledOnDisk(t) {
			quiet++
			if quiet >= 5 {
				return true
			}
			continue
		}
		quiet = 0
	}
	return false
}

// settledOnDisk 判断下载结果是不是已经落成正式名字。
// gopeed 在下载/校验期间用的是 <名字>.part，校验通过才换成正式名；只要还留着
// .part，就说明它仍在动那些文件，这时候改名就是往枪口上撞。
func settledOnDisk(t *gTask) bool {
	root, single, err := resolvePaths(t)
	if err != nil || root == "" {
		return false
	}
	if _, err := os.Stat(root); err != nil {
		return false
	}
	if single {
		if _, err := os.Stat(root + ".part"); err == nil {
			return false
		}
	}
	return true
}

// watchStray 改名后盯着旧路径有没有又冒出来。
// 正常情况什么都不会出现；真出现了说明 gopeed 还在做种并开始重下，只告警、
// 绝不动文件 —— 删文件正是把 gopeed 搞崩的那一下。
func watchStray(old, dst string) {
	if old == "" || dst == "" || filepath.Clean(old) == filepath.Clean(dst) {
		return
	}
	dir := filepath.Dir(dst)
	cands := []string{
		filepath.Join(dir, filepath.Base(old)),
		filepath.Join(dir, filepath.Base(old)+".part"),
	}
	for _, wait := range []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second, 60 * time.Second} {
		time.Sleep(wait)
		for _, p := range cands {
			if _, err := os.Stat(p); err == nil {
				log.Printf("警告：改名后 %s 又出现了，gopeed 可能还在做种"+
					"（把 bt.seedTime 调小、bt.seedRatio 调 0 可避免）", p)
			}
		}
	}
}

// ---------- HTTP ----------

type pushReq struct {
	URL      string `json:"url"`
	Dir      string `json:"dir"`
	Name     string `json:"name"`
	Password string `json:"password"` // 这个压缩包专用的解压密码，会排在服务端密码表之前
	Token    string `json:"token"`
}

type apiResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiResp{Code: 1000, Msg: msg})
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (r *registry) handlePush(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "只支持 GET / POST")
			return
		}
		var in pushReq
		if req.Method == http.MethodPost {
			ct := req.Header.Get("Content-Type")
			if strings.Contains(ct, "application/json") {
				if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(&in); err != nil {
					fail(w, http.StatusBadRequest, "JSON 解析失败："+err.Error())
					return
				}
			} else {
				_ = req.ParseForm()
				in = fromForm(req.Form)
			}
		} else {
			in = fromForm(req.URL.Query())
		}

		// 令牌：没配 PUSH_TOKEN 就完全放开（面板那边可以另外加登录）
		if cfg.token != "" && in.Token != cfg.token && req.Header.Get("X-Push-Token") != cfg.token {
			fail(w, http.StatusUnauthorized, "推送令牌不正确")
			return
		}

		u := strings.TrimSpace(in.URL)
		if u == "" {
			fail(w, http.StatusBadRequest, "缺少 url")
			return
		}
		if !supportedURL(u) {
			fail(w, http.StatusBadRequest, "链接格式不支持：只接受 http(s)://、magnet:?、ed2k://、data:application/x-bittorrent")
			return
		}
		dir := strings.TrimSpace(in.Dir)
		name := strings.TrimSpace(in.Name)
		if name != "" {
			clean, err := sanitizeName(name)
			if err != nil {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
			name = clean
		}

		id, err := r.cli.create(u, dir, strings.TrimSpace(in.Password))
		if err != nil {
			fail(w, http.StatusBadGateway, "创建任务失败："+err.Error())
			return
		}
		j := &job{
			ID: id, URL: u, Dir: dir, Name: name,
			Status: "pending", Message: "已加入下载队列",
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		r.add(j)
		if name != "" {
			go r.watch(j)
		} else {
			r.update(id, func(x *job) { x.Status = "done"; x.Message = "已加入下载队列（未指定重命名）" })
		}
		writeJSON(w, http.StatusOK, apiResp{Code: 0, Data: j})
	}
}

func fromForm(v url.Values) pushReq {
	return pushReq{
		URL:      v.Get("url"),
		Dir:      v.Get("dir"),
		Name:     v.Get("name"),
		Password: v.Get("password"),
		Token:    v.Get("token"),
	}
}

// supportedURL 做最基本的协议判断，真正的校验交给 gopeed。
func supportedURL(u string) bool {
	if strings.HasPrefix(u, "magnet:?") || strings.HasPrefix(u, "ed2k://") {
		return true
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return true
	}
	if strings.HasPrefix(u, "data:application/x-bittorrent") {
		return true
	}
	return false
}

func (r *registry) handleStatus(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get("id")
	if id == "" {
		fail(w, http.StatusBadRequest, "缺少 id")
		return
	}
	j, ok := r.get(id)
	if !ok {
		fail(w, http.StatusNotFound, "没有这个任务")
		return
	}
	// 顺手补上 gopeed 侧的实时状态，省得调用方再查一次
	out := map[string]any{"job": j}
	if t, err := r.cli.task(id); err == nil {
		out["status"] = t.Status
		out["name"] = t.Name
		if t.Progress != nil {
			out["downloaded"] = t.Progress.Downloaded
			out["speed"] = t.Progress.Speed
		}
		if t.Meta != nil && t.Meta.Res != nil {
			out["size"] = t.Meta.Res.Size
		}
	}
	writeJSON(w, http.StatusOK, apiResp{Code: 0, Data: out})
}

// handleCleanup 在面板「删除任务和文件」之后被调用。
//
// 为什么需要它：任务下载完被改名后，gopeed 记录的还是旧路径，它自己的
// force 删除只能删到旧名字，改名后的文件会留在盘上。这里按记录的 finalPath
// 把那份文件补删掉。
func (r *registry) handleCleanup(cfg config, w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost && req.Method != http.MethodDelete {
		fail(w, http.StatusMethodNotAllowed, "只支持 POST / DELETE")
		return
	}
	id := req.URL.Query().Get("id")
	if id == "" {
		fail(w, http.StatusBadRequest, "缺少 id")
		return
	}
	if cfg.token != "" && req.Header.Get("X-Push-Token") != cfg.token {
		fail(w, http.StatusUnauthorized, "推送令牌不正确")
		return
	}
	j, ok := r.get(id)
	if !ok || j.FinalPath == "" {
		writeJSON(w, http.StatusOK, apiResp{Code: 0, Data: map[string]any{"removed": ""}})
		return
	}
	p := j.FinalPath
	if err := guardPath(p, cfg.roots); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(p); err != nil {
		writeJSON(w, http.StatusOK, apiResp{Code: 0, Data: map[string]any{"removed": "", "note": "文件已不在"}})
		return
	}
	if err := os.RemoveAll(p); err != nil {
		fail(w, http.StatusInternalServerError, "删除失败："+err.Error())
		return
	}
	r.update(id, func(x *job) { x.FinalPath = "" })
	writeJSON(w, http.StatusOK, apiResp{Code: 0, Data: map[string]any{"removed": p}})
}

// guardPath 只允许删配置里列出的根目录之下、且不是根本身的东西。
func guardPath(p string, roots []string) error {
	abs, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return err
	}
	for _, root := range roots {
		ra, err := filepath.Abs(filepath.Clean(root))
		if err != nil || ra == "" {
			continue
		}
		if abs == ra {
			return errors.New("拒绝删除下载根目录本身")
		}
		if strings.HasPrefix(abs, ra+string(os.PathSeparator)) {
			return nil
		}
	}
	return fmt.Errorf("路径 %s 不在允许删除的目录里（PUSH_ROOTS）", abs)
}

func (r *registry) handleList(w http.ResponseWriter, req *http.Request) {	r.mu.Lock()
	list := make([]*job, 0, len(r.jobs))
	for _, j := range r.jobs {
		cp := *j
		list = append(list, &cp)
	}
	r.mu.Unlock()
	sort.Slice(list, func(i, k int) bool { return list[i].CreatedAt.After(list[k].CreatedAt) })
	writeJSON(w, http.StatusOK, apiResp{Code: 0, Data: list})
}

func main() {
	cfg := loadConfig()
	cli := newGClient(cfg.gopeedURL)
	reg := newRegistry(cfg.statePath, cli)

	// 重启后把还没改完名的任务接着盯上
	for _, j := range reg.pending() {
		if j.Name != "" {
			go reg.watch(j)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/push", reg.handlePush(cfg))
	mux.HandleFunc("/status", reg.handleStatus)
	mux.HandleFunc("/jobs", reg.handleList)
	mux.HandleFunc("/cleanup", func(w http.ResponseWriter, req *http.Request) {
		reg.handleCleanup(cfg, w, req)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, apiResp{Code: 0, Data: map[string]any{
			"ok": true, "gopeed": cfg.gopeedURL, "auth": cfg.token != "",
		}})
	})

	srv := &http.Server{
		Addr:              cfg.listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("gopeed-push 监听 %s，转发到 %s", cfg.listen, cfg.gopeedURL)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
