// Gopeed Web UI —— 用 MyGo 的原生 UI 写成，编译到 WebAssembly 后在浏览器里跑。
// 界面由 Go 画（WebGL2 canvas），数据来自远端 gopeed 的 REST API。
package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const defaultServer = "http://192.0.2.10:9999"

// resolveServer 决定用哪个服务器地址：优先 localStorage 里存的，
// 否则用页面自身的来源（部署时同源反代 /api，最省心）。
func resolveServer() string {
	if v := loadStr("gopeed.server"); strings.TrimSpace(v) != "" {
		return v
	}
	if o := pageOrigin(); o != "" && (strings.HasPrefix(o, "http://") || strings.HasPrefix(o, "https://")) {
		return o
	}
	return defaultServer
}

// TaskView 是界面用的任务视图，已把 gopeed 的原始字段算成人类可读的值。
type TaskView struct {
	ID          string
	Name        string
	Protocol    string
	Status      string
	Size        int64
	Downloaded  int64
	Percent     float64
	Speed       int64
	Uploaded    int64
	UploadSpeed int64
	Peers       int
	Connections int
	Path        string
}

// taskDetail 是详情弹窗的数据：完整的任务对象 + 实时统计（stats 只有运行中的任务才有）。
type taskDetail struct {
	Task  *gTask
	Stats map[string]any
	Err   string
}

// app 是全部状态。除明确标出的字段外，只在主线程读写。
type app struct {
	server string
	token  string

	// 任务快照（主线程）
	tasks  []TaskView
	loaded bool
	ok     bool
	errMsg string

	// 界面状态（主线程）
	filter int // 0 全部 1 下载中 2 已完成
	query  string
	list   ui.ListState
	toast  string

	// 新建任务弹窗
	showNew  bool
	newLink  string
	newPath  string
	creating bool

	// 登录（面板设了访问密码时）
	needAuth  bool
	loggingIn bool
	loginUser string
	loginPass string
	loginErr  string

	// 删除确认弹窗
	delAsk       bool
	delID        string
	delName      string
	delWithFiles bool

	// 任务详情弹窗
	showDetail bool
	detailID   string
	detailName string
	detail     *taskDetail
	detailBusy bool
	detailErr  string

	// 设置弹窗
	booted  bool
	showSet bool
	setTab  int  // 0 常规 1 HTTP 2 BT 3 通知 4 连接
	setBusy bool // 正在读/写服务器配置
	setMsg  string
	srvInfo string // 服务器信息（版本/系统/架构）

	// extra 是 gopeed 给扩展用的自由字段，界面不展示，但要原样带回去，
	// 否则 PUT 整份替换会把它清空。
	extraCfg map[string]any

	// 常规
	fDir            string
	fMaxText        string
	fTorrent        bool
	fTorrentDel     bool
	fExtract        bool
	fExtractDel     bool
	fPwList         string // 解压密码表，一行一个
	fAutoDelMissing bool

	// HTTP
	fHttpConn  string
	fHttpUA    string
	fHttpCtime bool

	// BT
	fBtPort     string
	fBtRatio    string
	fBtTime     string
	fBtKeep     bool
	fBtTrackers string

	// 做种自动清理与启动续传（本机补丁）
	fSeedClean     bool
	fSeedRatio     string
	fSeedIdle      string
	fResumeOnStart bool

	// 面板不编辑的路径映射，保存时原样带回去
	pathMappings []gPathMapping

	// 代理（界面在 HTTP 页里，不再单独开一个标签页）
	fProxyAll      bool
	fProxyPartial  bool
	fProxyKeywords string
	fProxySystem   bool
	fProxyScheme   string
	fProxyHost     string
	fProxyUser     string
	fProxyPass     string

	// 通知与脚本
	fHookEnable   bool
	fHookURLs     string
	fScriptEnable bool
	fScriptPaths  string

	win *mygo.Window
}

func (a *app) onMain(fn func()) { mygo.RunOnMain(fn) }

// firstPaint 在第一次拿到数据（成功或失败）后隐藏页面加载遮罩，只做一次。
func (a *app) firstPaint() {
	if a.booted {
		return
	}
	a.booted = true
	hideBoot()
}

// ---------- 数据刷新 ----------

// refresh 在后台 goroutine 里跑：抓任务、算视图，再回主线程写状态并重绘。
func (a *app) refresh(silent bool) {
	cli := newClient(a.server, a.token)
	ts, err := cli.tasks()
	if err != nil {
		unauthorized := errors.Is(err, errUnauthorized)
		a.onMain(func() {
			a.ok = false
			if unauthorized {
				// 面板设了访问密码，切到登录页，别显示成「连不上」
				a.needAuth = true
				a.errMsg = ""
			} else {
				a.errMsg = err.Error()
			}
			if !silent {
				a.loaded = true
			}
			a.firstPaint()
			if a.win != nil {
				a.win.Invalidate()
			}
		})
		return
	}

	views := make([]TaskView, 0, len(ts))
	for _, t := range ts {
		v := TaskView{ID: t.ID, Name: t.Name, Protocol: t.Protocol, Status: t.Status}
		if t.Meta != nil {
			if t.Meta.Res != nil {
				v.Size = t.Meta.Res.Size
				if v.Name == "" {
					v.Name = t.Meta.Res.Name
				}
				// HTTP 单文件资源 name/size 常为空，真值在 files[0]
				if v.Size == 0 && len(t.Meta.Res.Files) > 0 {
					v.Size = t.Meta.Res.Files[0].Size
					if v.Name == "" {
						v.Name = t.Meta.Res.Files[0].Name
					}
				}
			}
			if t.Meta.Opts != nil {
				v.Path = t.Meta.Opts.Path
			}
		}
		if t.Progress != nil {
			v.Downloaded = t.Progress.Downloaded
			v.Speed = t.Progress.Speed
			v.Uploaded = t.Progress.Uploaded
			v.UploadSpeed = t.Progress.UploadSpeed
		}
		if v.Size > 0 {
			v.Percent = float64(v.Downloaded) / float64(v.Size) * 100
		}
		if v.Status == "done" {
			v.Percent = 100
		}
		if t.Status == "running" || t.Status == "wait" {
			if st, err := cli.stats(t.ID); err == nil {
				applyStats(&v, st)
			}
		}
		views = append(views, v)
	}

	order := map[string]int{"running": 0, "wait": 1, "ready": 2, "pause": 3, "error": 4, "done": 5}
	sort.SliceStable(views, func(i, j int) bool {
		oi, oj := order[views[i].Status], order[views[j].Status]
		if oi != oj {
			return oi < oj
		}
		return views[i].Name < views[j].Name
	})

	a.onMain(func() {
		a.tasks = views
		a.loaded = true
		a.ok = true
		a.errMsg = ""
		a.firstPaint()
		if a.win != nil {
			a.win.Invalidate()
		}
	})
}

func applyStats(v *TaskView, st map[string]any) {
	if st == nil {
		return
	}
	if _, isBT := st["totalPeers"]; isBT {
		if n, ok := st["activePeers"].(float64); ok {
			v.Peers = int(n)
		}
		if v.Peers == 0 {
			if n, ok := st["totalPeers"].(float64); ok {
				v.Peers = int(n)
			}
		}
	}
	if arr, ok := st["connections"].([]any); ok {
		v.Connections = len(arr)
	}
}

// poll 每 1.5 秒刷一次。
func (a *app) poll() {
	for range time.Tick(1500 * time.Millisecond) {
		if a.showDetail || a.detailBusy {
			continue
		}
		a.refresh(true)
	}
}

// ---------- 视图 ----------

func (a *app) view(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		a.header(c, t)
		if !a.needAuth {
			a.toolbar(c, t)
		}
		a.body(c, t)
		if a.toast != "" {
			ui.Row(c).Padding(10, 18).Background(t.Surface).BorderWidth(1, 0, 0, 0).
				BorderColor(t.Border).Children(func() {
				ui.Text(c, a.toast).FontSize(12).TextColor(t.TextMuted).SingleLine()
			})
		}
	})
	a.newDialog(c, t)
	a.delDialog(c, t)
	a.detailDialog(c, t)
	a.setDialog(c, t)
}

func (a *app) header(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Padding(14, 18).Gap(8).AlignItems(ui.Center).Background(t.Surface).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		dot, txt := t.TextMuted, "连接中…"
		if a.needAuth {
			dot, txt = ui.RGBA(255, 159, 10, 1), "需要登录"
		} else if a.loaded {
			if a.ok {
				dot, txt = ui.RGBA(52, 199, 89, 1), "已连接"
			} else {
				dot, txt = t.Danger, "连接失败"
			}
		}
		ui.Box(c).Size(8, 8).Radius(999).Background(dot)
		ui.Text(c, "Gopeed").FontSize(15).Bold()
		ui.Text(c, "v2").FontSize(10).TextColor(t.TextMuted)
		ui.Text(c, txt).FontSize(12).TextColor(t.TextMuted)
		ui.Spacer(c)
		if a.loaded && !a.ok && a.errMsg != "" {
			ui.Text(c, a.errMsg).FontSize(12).TextColor(t.Danger).SingleLine().MaxWidth(340)
			ui.Spacer(c)
		}
		ui.Text(c, a.summary()).FontSize(12).TextColor(t.TextMuted)
	})
}

func (a *app) summary() string {
	running, done := 0, 0
	var speed int64
	for _, v := range a.tasks {
		switch v.Status {
		case "running", "wait", "ready":
			running++
			speed += v.Speed
		case "done":
			done++
		}
	}
	if len(a.tasks) == 0 {
		return "无任务"
	}
	return fmt.Sprintf("%s/s · %d 个下载中 · %d 个已完成", human(speed), running, done)
}

func (a *app) toolbar(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Padding(12, 18).Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.PrimaryButton(c, "＋ 新建任务").Clicked() {
			a.openNew()
		}
		ui.Segmented(c, &a.filter, "全部", "下载中", "已完成")
		ui.Spacer(c)
		if ui.Button(c, "全部暂停").Clicked() {
			go a.bulk("pause")
		}
		if ui.Button(c, "全部继续").Clicked() {
			go a.bulk("continue")
		}
		if ui.Button(c, "刷新").Clicked() {
			go a.refresh(false)
		}
		ui.TextInput(c, &a.query).Placeholder("搜索任务").Width(180)
		if ui.Button(c, "设置").Clicked() {
			a.openSettings()
		}
	})
}

func (a *app) body(c *ui.Context, t *ui.Theme) {
	if a.needAuth {
		a.loginView(c, t)
		return
	}
	rows := a.visible()
	a.list.Key = func(i int) any { return rows[i].ID }
	ui.List(c, &a.list, len(rows), func(i int) {
		a.row(c, t, rows[i])
	}).Grow(1).Padding(12, 18).Gap(8).Children(func() {
		hint := "还没有下载任务"
		if !a.loaded {
			hint = "正在加载…"
		} else if !a.ok {
			hint = "连不上服务器，检查设置"
		} else if len(a.tasks) > 0 {
			hint = "没有符合条件的任务"
		}
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, hint).FontSize(14).TextColor(t.TextMuted)
		})
	})
}

// loginView 是面板设了访问密码时显示的登录页。
func (a *app) loginView(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Center().Children(func() {
		ui.Column(c).Width(360).Gap(12).Padding(24, 26).Radius(12).
			Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Text(c, "登录 Gopeed").FontSize(18).Bold()
			ui.Text(c, "这个面板设了访问密码，输入后即可使用。").
				FontSize(12).TextColor(t.TextMuted)
			ui.Divider(c)
			a.label(c, t, "用户名")
			ui.TextInput(c, &a.loginUser).Placeholder("用户名")
			a.label(c, t, "密码")
			passIn := ui.TextInput(c, &a.loginPass).Placeholder("密码")
			if a.loginErr != "" {
				ui.Text(c, a.loginErr).FontSize(12).TextColor(t.Danger)
			}
			btn := ui.PrimaryButton(c, "登录")
			if a.loggingIn {
				btn.Disabled(true)
			}
			if btn.Clicked() || passIn.Submitted() {
				a.doLogin()
			}
		})
	})
}

// doLogin 用「用户名:密码」的 base64 作为面板密钥试一次。
func (a *app) doLogin() {
	if a.loggingIn {
		return
	}
	user := strings.TrimSpace(a.loginUser)
	if user == "" || a.loginPass == "" {
		a.loginErr = "用户名和密码都要填"
		return
	}
	a.loggingIn = true
	a.loginErr = ""
	key := base64.StdEncoding.EncodeToString([]byte(user + ":" + a.loginPass))
	go func() {
		old := panelKey
		panelKey = key
		cli := newClient(a.server, a.token)
		info, err := cli.info()
		a.onMain(func() {
			a.loggingIn = false
			if err != nil {
				panelKey = old
				if errors.Is(err, errUnauthorized) {
					a.loginErr = "用户名或密码不对"
				} else {
					a.loginErr = "连不上服务器：" + err.Error()
				}
				a.invalidate()
				return
			}
			saveStr("gopeed.panelkey", key)
			a.srvInfo = info
			a.loginPass = ""
			a.needAuth = false
			a.invalidate()
			go a.refresh(false)
		})
	}()
}

func (a *app) invalidate() {
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *app) row(c *ui.Context, t *ui.Theme, v TaskView) {
	ui.Column(c).Gap(8).Padding(12, 14).Radius(10).Background(t.Surface).
		Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			label := strings.ToUpper(v.Protocol)
			if label == "" {
				label = "?"
			}
			ui.Text(c, label).FontSize(10).Bold().TextColor(protoColor(v.Protocol, t))
			ui.Text(c, v.Name).FontSize(14).SingleLine().Grow(1)
			ui.Text(c, statusText(v.Status)).FontSize(12).TextColor(statusColor(v.Status, t))
		})

		p := v.Percent
		if p < 0 {
			p = 0
		}
		if p > 100 {
			p = 100
		}
		ui.Progress(c, p/100).Height(6)

		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			meta := fmt.Sprintf("%s / %s", human(v.Downloaded), human(v.Size))
			if v.Speed > 0 {
				meta += "  ↓ " + human(v.Speed) + "/s"
			}
			if v.UploadSpeed > 0 {
				meta += "  ↑ " + human(v.UploadSpeed) + "/s"
			}
			if v.Peers > 0 {
				meta += fmt.Sprintf("  节点 %d", v.Peers)
			}
			if v.Connections > 0 {
				meta += fmt.Sprintf("  连接 %d", v.Connections)
			}
			if v.Path != "" {
				meta += "  " + v.Path
			}
			ui.Text(c, meta).FontSize(11).TextColor(t.TextMuted).SingleLine().Grow(1)
			ui.Spacer(c)

			if ui.Button(c, "详情").Clicked() {
				a.openDetail(v.ID, v.Name)
			}
			switch v.Status {
			case "running", "wait", "ready":
				if ui.Button(c, "暂停").Clicked() {
					a.act(v.ID, "pause")
				}
			case "pause", "error":
				if ui.Button(c, "继续").Clicked() {
					a.act(v.ID, "continue")
				}
			}
			if ui.Button(c, "删除").Clicked() {
				a.remove(v.ID, v.Name)
			}
		})
	})
}

func (a *app) visible() []TaskView {
	out := make([]TaskView, 0, len(a.tasks))
	q := strings.ToLower(strings.TrimSpace(a.query))
	for _, v := range a.tasks {
		switch a.filter {
		case 1:
			if v.Status != "running" && v.Status != "wait" && v.Status != "ready" {
				continue
			}
		case 2:
			if v.Status != "done" {
				continue
			}
		}
		if q != "" && !strings.Contains(strings.ToLower(v.Name), q) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// ---------- 动作 ----------

func (a *app) toastOn(s string) {
	a.onMain(func() {
		a.toast = s
		if a.win != nil {
			a.win.Invalidate()
		}
	})
}

func (a *app) act(id, what string) {
	go func() {
		if err := newClient(a.server, a.token).action(id, what); err != nil {
			a.toastOn("操作失败：" + err.Error())
		}
		time.Sleep(200 * time.Millisecond)
		a.refresh(true)
	}()
}

// remove 先弹确认框，让用户决定是只删任务还是连磁盘上的文件一起删。
func (a *app) remove(id, name string) {
	a.delID = id
	a.delName = name
	a.delWithFiles = false
	a.delAsk = true
}

// doDelete 执行删除；withFiles 为真时带 force=true，gopeed 会连文件一起删。
func (a *app) doDelete() {
	id, with := a.delID, a.delWithFiles
	a.delAsk = false
	go func() {
		cli := newClient(a.server, a.token)
		if err := cli.del(id, with); err != nil {
			a.toastOn("删除失败：" + err.Error())
		} else if with {
			// 改名已经由 gopeed 本体完成：它把任务记录里的名字一起改了，
			// 所以 force 删除删的就是改名后那份，不需要再补一刀。
			a.toastOn("任务和本地文件已删除")
		} else {
			a.toastOn("任务已删除（保留文件）")
		}
		time.Sleep(150 * time.Millisecond)
		a.refresh(true)
	}()
}

// openDetail 打开详情弹窗：先在后台取数，数据回来再弹。
//
// 两条约束决定了这个流程：①wasm 里在主线程同步发 HTTP 会把事件循环卡死；
// ②ui.Modal 开着时它跑自己的嵌套循环，别的 goroutine 投给 RunOnMain 的函数
// 不会被处理。所以弹窗一律等数据到了才显示，读取期间只给一条提示。
func (a *app) openDetail(id, name string) {
	a.detailID = id
	a.detailName = name
	a.detail = nil
	a.detailErr = ""
	a.detailBusy = true
	a.showDetail = false
	a.toastOn("正在读取详情…")
	a.loadDetailAsync()
}

// loadDetailAsync 拉任务详情与实时统计，回到主线程后填状态并弹窗。
func (a *app) loadDetailAsync() {
	id := a.detailID
	if id == "" {
		return
	}
	go func() {
		cli := newClient(a.server, a.token)
		d := &taskDetail{}
		if t, err := cli.task(id); err != nil {
			d.Err = err.Error()
		} else {
			d.Task = t
			// stats 只有活着的任务才有，失败不算错
			if st, err := cli.stats(id); err == nil {
				d.Stats = st
			}
		}
		a.onMain(func() {
			if a.detailID != id {
				return
			}
			a.detail = d
			a.detailErr = d.Err
			a.detailBusy = false
			a.showDetail = true
			a.toast = ""
			a.invalidate()
		})
	}()
}

// refreshDetail 刷新当前详情：先收起弹窗（它开着时回调进不来），数据回来再弹。
func (a *app) refreshDetail() {
	if a.detailBusy {
		return
	}
	a.showDetail = false
	a.detailBusy = true
	a.invalidate()
	a.loadDetailAsync()
}

func (a *app) bulk(what string) {
	cli := newClient(a.server, a.token)
	ts, err := cli.tasks()
	if err != nil {
		a.toastOn("取任务失败：" + err.Error())
		return
	}
	for _, t := range ts {
		_ = cli.action(t.ID, what)
	}
	a.refresh(true)
}

func (a *app) create() {
	links := strings.Split(strings.ReplaceAll(a.newLink, "\r\n", "\n"), "\n")
	path := strings.TrimSpace(a.newPath)
	a.showNew = false
	a.creating = true
	go func() {
		cli := newClient(a.server, a.token)
		var errs []string
		n := 0
		for _, link := range links {
			link = strings.TrimSpace(link)
			if link == "" {
				continue
			}
			var opts *gOptions
			if path != "" {
				opts = &gOptions{Path: path}
			}
			if _, err := cli.create(&gRequest{URL: link}, opts); err != nil {
				errs = append(errs, err.Error())
			} else {
				n++
			}
		}
		a.onMain(func() {
			a.creating = false
			a.newLink = ""
		})
		if len(errs) > 0 {
			a.toastOn(fmt.Sprintf("成功 %d 个，失败 %d 个：%s", n, len(errs), errs[0]))
		} else {
			a.toastOn(fmt.Sprintf("已添加 %d 个任务", n))
		}
		time.Sleep(300 * time.Millisecond)
		a.refresh(true)
	}()
}

func (a *app) openNew() {
	a.showNew = true
	if a.newPath == "" {
		a.newPath = a.fDir
	}
}

func (a *app) openSettings() {
	a.showSet = true
	a.setMsg = "正在读取服务器配置…"
	a.setBusy = true
	server, token := a.server, a.token
	go func() {
		cli := newClient(server, token)
		cfg, err := cli.config()
		info, ierr := cli.info()
		a.onMain(func() {
			a.setBusy = false
			if ierr == nil {
				a.srvInfo = info
			}
			if err != nil {
				a.setMsg = "读取配置失败：" + err.Error()
				return
			}
			a.setMsg = ""
			a.loadConfig(cfg)
		})
	}()
}

// primeConfig 在启动时静默读一次服务器配置，只为了让「新建任务」的保存路径
// 预填成服务器默认目录；不弹窗、不覆盖用户正在编辑的表单。
func (a *app) primeConfig() {
	cfg, err := newClient(a.server, a.token).config()
	if err != nil {
		return
	}
	a.onMain(func() {
		if a.showSet {
			return // 用户已经打开设置，别抢他的编辑
		}
		a.loadConfig(cfg)
	})
}

// loadConfig 把服务器配置铺到表单字段上。
func (a *app) loadConfig(cfg *gConfig) {
	a.extraCfg = cfg.Extra
	a.fDir = cfg.DownloadDir
	a.fMaxText = strconv.Itoa(orInt(cfg.MaxRunning, 5))

	at := cfg.AutoTorrent
	if at == nil {
		at = &gAutoTorrent{}
	}
	a.fTorrent = at.Enable
	a.fTorrentDel = at.DeleteAfterDownload

	ar := cfg.Archive
	if ar == nil {
		ar = &gArchive{}
	}
	a.fExtract = ar.AutoExtract
	a.fExtractDel = ar.DeleteAfterExtract
	a.fPwList = strings.Join(ar.Passwords, "\n")

	a.fAutoDelMissing = cfg.AutoDeleteMissingFileTasks

	h := cfg.httpConf()
	a.fHttpConn = strconv.Itoa(h.Connections)
	a.fHttpUA = h.UserAgent
	a.fHttpCtime = h.UseServerCtime

	bt := cfg.btConf()
	a.fBtPort = strconv.Itoa(bt.ListenPort)
	a.fBtRatio = trimFloat(bt.SeedRatio)
	a.fBtTime = strconv.FormatInt(bt.SeedTime, 10)
	a.fBtKeep = bt.SeedKeep
	a.fBtTrackers = strings.Join(bt.Trackers, "\n")

	sr := cfg.SeedAutoRemove
	if sr == nil {
		sr = &gSeedAutoRemove{}
	}
	a.fSeedClean = sr.Enable
	a.fSeedRatio = trimFloat(sr.Ratio)
	a.fSeedIdle = strconv.Itoa(sr.IdleMinutes)
	// 服务端没这个键时默认开启（与补丁的默认值一致）
	a.fResumeOnStart = cfg.ResumeOnStart == nil || *cfg.ResumeOnStart
	a.pathMappings = cfg.PathMappings

	px := cfg.Proxy
	if px == nil {
		px = &gProxy{}
	}
	mode := px.Mode
	if mode == "" {
		// 老配置只有 enable
		if px.Enable {
			mode = "all"
		} else {
			mode = "off"
		}
	}
	a.fProxyAll = mode == "all"
	a.fProxyPartial = mode == "partial"
	a.fProxyKeywords = strings.Join(px.Keywords, "\n")
	a.fProxySystem = px.System
	a.fProxyScheme = px.Scheme
	a.fProxyHost = px.Host
	a.fProxyUser = px.Usr
	a.fProxyPass = px.Pwd

	hk := cfg.Webhook
	if hk == nil {
		hk = &gWebhook{}
	}
	a.fHookEnable = hk.Enable
	a.fHookURLs = strings.Join(hk.URLs, "\n")

	sc := cfg.Script
	if sc == nil {
		sc = &gScript{}
	}
	a.fScriptEnable = sc.Enable
	a.fScriptPaths = strings.Join(sc.Paths, "\n")
}

// collectConfig 把表单字段拼成完整配置。PUT /api/v1/config 是整份替换，
// 所以这里必须把所有字段都填上，漏一个就会把它清空。
func (a *app) collectConfig() *gConfig {
	resume := a.fResumeOnStart
	cfg := &gConfig{
		DownloadDir:                strings.TrimSpace(a.fDir),
		MaxRunning:                 atoiOr(a.fMaxText, 5),
		Extra:                      a.extraCfg,
		AutoDeleteMissingFileTasks: a.fAutoDelMissing,
		SeedAutoRemove: &gSeedAutoRemove{
			Enable:      a.fSeedClean,
			Ratio:       atofOr(a.fSeedRatio, 0),
			IdleMinutes: atoiOr(a.fSeedIdle, 0),
		},
		ResumeOnStart: &resume,
		PathMappings:  a.pathMappings,
		AutoTorrent: &gAutoTorrent{
			Enable:              a.fTorrent,
			DeleteAfterDownload: a.fTorrentDel,
		},
		Archive: &gArchive{
			AutoExtract:        a.fExtract,
			DeleteAfterExtract: a.fExtractDel,
			Passwords:          splitLines(a.fPwList),
		},
		Proxy: func() *gProxy {
			// 两个开关同时打开时按「部分」处理（更保守，不会把所有流量都塞进代理）
			mode := "off"
			switch {
			case a.fProxyPartial:
				mode = "partial"
			case a.fProxyAll:
				mode = "all"
			}
			return &gProxy{
				Enable:   mode != "off",
				Mode:     mode,
				Keywords: splitLines(a.fProxyKeywords),
				System:   a.fProxySystem,
				Scheme:   strings.TrimSpace(a.fProxyScheme),
				Host:     strings.TrimSpace(a.fProxyHost),
				Usr:      a.fProxyUser,
				Pwd:      a.fProxyPass,
			}
		}(),
		Webhook: &gWebhook{Enable: a.fHookEnable, URLs: splitLines(a.fHookURLs)},
		Script:  &gScript{Enable: a.fScriptEnable, Paths: splitLines(a.fScriptPaths)},
	}
	if cfg.MaxRunning <= 0 {
		cfg.MaxRunning = 5
	}
	// 代理开着但 scheme 留空时给个默认，否则 gopeed 建不出 proxy URL；
	// 没开代理就不要改动这个字段，避免无谓的配置漂移。
	if cfg.Proxy.Enable && cfg.Proxy.Scheme == "" {
		cfg.Proxy.Scheme = "http"
	}

	cfg.setProto("http", gHTTPConf{
		UserAgent:      strings.TrimSpace(a.fHttpUA),
		Connections:    atoiOr(a.fHttpConn, 16),
		UseServerCtime: a.fHttpCtime,
	})
	cfg.setProto("bt", gBTConf{
		ListenPort: atoiOr(a.fBtPort, 0),
		Trackers:   splitLines(a.fBtTrackers),
		SeedKeep:   a.fBtKeep,
		SeedRatio:  atofOr(a.fBtRatio, 1),
		SeedTime:   int64(atoiOr(a.fBtTime, 7200)),
	})
	return cfg
}

func (a *app) saveSettings() {
	if a.setBusy {
		return
	}
	// 本地设置先落盘，这样刷新页面不丢
	saveStr("gopeed.server", strings.TrimSpace(a.server))
	saveStr("gopeed.token", strings.TrimSpace(a.token))
	server, token := a.server, a.token
	payload := a.collectConfig()
	a.setBusy = true
	a.setMsg = "正在保存…"
	go func() {
		err := newClient(server, token).putConfig(payload)
		a.onMain(func() {
			a.setBusy = false
			if err != nil {
				a.setMsg = "保存失败：" + err.Error()
				a.toastOn("保存失败：" + err.Error())
				return
			}
			a.setMsg = "已保存"
			a.toastOn("设置已保存")
			// 服务器会做范围校验（比如并发数），重新读一遍以显示真实值
			a.refresh(true)
			a.reloadConfig(server, token)
		})
	}()
}

// reloadConfig 保存后回读一次，把服务器规范化后的值同步回表单。
func (a *app) reloadConfig(server, token string) {
	go func() {
		cfg, err := newClient(server, token).config()
		if err != nil {
			return
		}
		a.onMain(func() {
			if a.showSet {
				a.loadConfig(cfg)
			}
		})
	}()
}

// ---------- 弹窗 ----------

func (a *app) newDialog(c *ui.Context, t *ui.Theme) {
	ui.Modal(c, &a.showNew, func() {
		ui.Text(c, "新建下载").FontSize(17).Bold()
		ui.Text(c, "支持 http/https、磁力链、.torrent 链接。一行一个，可批量。").FontSize(12).TextColor(t.TextMuted)
		ui.TextArea(c, &a.newLink).Placeholder("https://example.com/file.iso\nmagnet:?xt=urn:btih:…").Height(120).AutoFocus()
		ui.Text(c, "保存路径（留空用服务器默认目录）").FontSize(12).TextColor(t.TextMuted)
		ui.TextInput(c, &a.newPath).Placeholder("/downloads")
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				a.showNew = false
			}
			btn := ui.PrimaryButton(c, "开始下载")
			if a.creating {
				btn = ui.PrimaryButton(c, "添加中…")
			}
			if btn.Clicked() && !a.creating {
				a.create()
			}
		})
	})
}

// delDialog 是删除前的确认框：默认只删任务，勾选后才连本地文件一起删。
func (a *app) delDialog(c *ui.Context, t *ui.Theme) {
	ui.Modal(c, &a.delAsk, func() {
		ui.Column(c).Width(460).Gap(12).Children(func() {
			ui.Text(c, "删除任务").FontSize(17).Bold()
			name := a.delName
			if name == "" {
				name = a.delID
			}
			ui.Text(c, name).FontSize(13).TextColor(t.TextMuted).SingleLine()
			ui.Divider(c)
			ui.Checkbox(c, &a.delWithFiles, "同时删除已下载的本地文件")
			if a.delWithFiles {
				ui.Text(c, "文件会被直接删除，不经过回收站，无法撤销。").
					FontSize(12).TextColor(t.Danger)
			} else {
				ui.Text(c, "只删掉这条任务记录，磁盘上的文件会保留。").
					FontSize(12).TextColor(t.TextMuted)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					a.delAsk = false
				}
				label := "删除任务"
				if a.delWithFiles {
					label = "删除任务和文件"
				}
				btn := ui.PrimaryButton(c, label)
				if a.delWithFiles {
					btn.Background(t.Danger)
				}
				if btn.Clicked() {
					a.doDelete()
				}
			})
		})
	})
}

// detailDialog 是任务详情：把 gopeed 的 meta 与 stats 摊开，BT / HTTP 各显示
// 自己那几项关键信息，文件列表单独滚动。
func (a *app) detailDialog(c *ui.Context, t *ui.Theme) {
	ui.Modal(c, &a.showDetail, func() {
		ui.Column(c).Width(700).Gap(10).Children(func() {
			name, proto := a.detailName, ""
			if a.detail != nil && a.detail.Task != nil {
				if v := a.detail.Task.Name; v != "" {
					name = v
				}
				proto = a.detail.Task.Protocol
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, strings.ToUpper(proto)).FontSize(10).Bold().TextColor(protoColor(proto, t))
				ui.Text(c, name).FontSize(16).Bold().SingleLine().Grow(1)
			})

			if a.detail == nil {
				ui.Text(c, "没有取到这条任务的详情。").
					FontSize(12).TextColor(t.TextMuted)
			}
			if a.detailErr != "" {
				ui.Text(c, a.detailErr).FontSize(12).TextColor(t.Danger)
			}
			if a.detail != nil && a.detail.Task != nil {
				a.detailBody(c, t, a.detail)
			}

			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "刷新").Clicked() {
					a.refreshDetail()
				}
				if ui.Button(c, "关闭").Clicked() {
					a.showDetail = false
				}
			})
		})
	})
}

// detailBody 画详情正文：左边进度类数字，右边路径/协议专属字段，下面是来源和文件列表。
func (a *app) detailBody(c *ui.Context, t *ui.Theme, d *taskDetail) {
	task := d.Task
	var size, done, speed, upSpeed int64
	if task.Meta != nil && task.Meta.Res != nil {
		size = task.Meta.Res.Size
	}
	if task.Progress != nil {
		done = task.Progress.Downloaded
		speed = task.Progress.Speed
		upSpeed = task.Progress.UploadSpeed
	}
	path := ""
	if task.Meta != nil && task.Meta.Opts != nil {
		path = task.Meta.Opts.Path
	}

	ui.Divider(c)
	ui.Row(c).Gap(20).Children(func() {
		ui.Column(c).Gap(5).Grow(1).Children(func() {
			a.kv(c, t, "状态", statusText(task.Status))
			a.kv(c, t, "进度", progressText(done, size))
			a.kv(c, t, "大小", human(size))
			a.kv(c, t, "已下载", human(done))
			a.kv(c, t, "下载速度", rateText(speed))
			a.kv(c, t, "上传速度", rateText(upSpeed))
		})
		ui.Column(c).Gap(5).Grow(1).Children(func() {
			switch task.Protocol {
			case "bt":
				a.kv(c, t, "节点", peersText(d.Stats))
				a.kv(c, t, "分享率", seedRatioText(d.Stats))
			case "http":
				a.kv(c, t, "断点续传", yesNo(taskRange(task)))
				a.kv(c, t, "连接数", connText(d.Stats))
			}
			a.kv(c, t, "创建时间", fmtTime(task.CreatedAt))
			a.kv(c, t, "任务 ID", task.ID)
		})
	})

	// 路径和 hash 都很长，单独占整行、允许折两行，别被省略号截掉
	if path != "" {
		a.kvWide(c, t, "保存路径", path)
	}
	if h := taskHash(task); h != "" {
		a.kvWide(c, t, "InfoHash", h)
	}
	if src := sourceText(task); src != "" {
		a.kvWide(c, t, "来源", src)
	}

	var files []gFileInfo
	if task.Meta != nil && task.Meta.Res != nil {
		files = task.Meta.Res.Files
	}
	if len(files) == 0 {
		return
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, fmt.Sprintf("文件（%d 个）", len(files))).FontSize(12).Bold().Grow(1)
		ui.Text(c, "合计 "+human(size)).FontSize(11).TextColor(t.TextMuted)
	})
	const maxFiles = 200
	n := len(files)
	if n > maxFiles {
		n = maxFiles
	}
	// 文件少的时候别留一大片空白
	listH := float32(n)*24 + 8
	if listH > 210 {
		listH = 210
	}
	if listH < 44 {
		listH = 44
	}
	ui.Scroll(c).Height(listH).Children(func() {
		ui.Column(c).Gap(4).Padding(0, 2, 0, 2).Children(func() {
			for i := 0; i < n; i++ {
				f := files[i]
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, f.Name).FontSize(11).Grow(1).SingleLine()
					ui.Text(c, human(f.Size)).FontSize(11).TextColor(t.TextMuted)
				})
			}
			if len(files) > n {
				ui.Text(c, fmt.Sprintf("…… 还有 %d 个文件没列出", len(files)-n)).
					FontSize(11).TextColor(t.TextMuted)
			}
		})
	})
}

func (a *app) setDialog(c *ui.Context, t *ui.Theme) {
	ui.Modal(c, &a.showSet, func() {
		ui.Column(c).Width(720).Gap(12).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
				ui.Text(c, "设置").FontSize(17).Bold().Grow(1)
				if a.srvInfo != "" {
					ui.Text(c, a.srvInfo).FontSize(12).TextColor(t.TextMuted)
				}
			})
			ui.Tabs(c, &a.setTab, "常规", "HTTP", "BT", "通知", "连接")
			ui.Scroll(c).Height(492).Children(func() {
				ui.Column(c).Gap(12).Padding(2, 4, 2, 4).Children(func() {
					switch a.setTab {
					case 0:
						a.tabGeneral(c, t)
					case 1:
						a.tabHTTP(c, t)
					case 2:
						a.tabBT(c, t)
					case 3:
						a.tabNotify(c, t)
					case 4:
						a.tabConn(c, t)
					}
				})
			})
			if a.setMsg != "" {
				ui.Text(c, a.setMsg).FontSize(12).TextColor(t.TextMuted)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "关闭").Clicked() {
					a.showSet = false
				}
				lbl := "保存"
				if a.setBusy {
					lbl = "处理中…"
				}
				btn := ui.PrimaryButton(c, lbl)
				btn.Disabled(a.setBusy)
				if btn.Clicked() && !a.setBusy {
					a.saveSettings()
				}
			})
		})
	})
}

// label 是一行小字说明。
func (a *app) label(c *ui.Context, t *ui.Theme, s string) {
	ui.Text(c, s).FontSize(12).TextColor(t.TextMuted)
}

// toggle 是「左边标题+说明、右边开关」的一行。
func (a *app) toggle(c *ui.Context, t *ui.Theme, title, hint string, v *bool) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, title).FontSize(13)
			if hint != "" {
				ui.Text(c, hint).FontSize(11).TextColor(t.TextMuted)
			}
		})
		ui.Switch(c, v).Label(title)
	})
}

// input 是「标题 + 单行输入框」，label 在上、框在下。
func (a *app) input(c *ui.Context, t *ui.Theme, title, placeholder string, v *string) {
	a.label(c, t, title)
	ui.TextInput(c, v).Placeholder(placeholder)
}

// multiline 是「标题 + 多行输入框」，用于一行一个的列表（tracker、webhook、脚本）。
// 必须用 TextArea：单行 TextInput 会把换行吃掉变成空格，列表就粘成一条了。
func (a *app) multiline(c *ui.Context, t *ui.Theme, title, placeholder string, h float32, v *string) {
	a.label(c, t, title)
	ui.TextArea(c, v).Placeholder(placeholder).Height(h)
}

// tabGeneral 常规：目录、并发、自动转种、自动解压、丢失文件任务。
func (a *app) tabGeneral(c *ui.Context, t *ui.Theme) {
	a.input(c, t, "默认下载目录", "/mnt/nas/dl", &a.fDir)
	a.label(c, t, "任务里的保存路径留空时用这个目录。注意别填 /tmp —— 那是内存盘。")

	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "最大并发任务数").FontSize(13)
			ui.Text(c, "同时在跑的下载数量，其余排队").FontSize(11).TextColor(t.TextMuted)
		})
		ui.TextInput(c, &a.fMaxText).Width(90)
	})
	ui.Divider(c)
	// 这条放在最前面：它只影响任务列表，跟下面的自动解压无关。
	// 解压那一组最后有个大文本框，放在末尾才不会把别的项挤出可视区。
	a.toggle(c, t, "自动删除文件已丢失的任务", "任务列表里那些文件被删掉的记录会被清掉", &a.fAutoDelMissing)
	ui.Divider(c)
	a.toggle(c, t, "下载完 .torrent 自动建 BT 任务", "抓到一个 .torrent 文件后自动加一个 BT 任务去下内容", &a.fTorrent)
	a.toggle(c, t, "建完 BT 任务后删掉 .torrent 文件", "仅在上一个开关打开时有效", &a.fTorrentDel)
	ui.Divider(c)
	a.toggle(c, t, "下载完自动解压压缩包", "支持 zip / tar / gz / 7z 等，解压到同名目录", &a.fExtract)
	a.toggle(c, t, "解压成功后删掉压缩包", "仅在上一个开关打开时有效", &a.fExtractDel)
	a.multiline(c, t, "解压密码（一行一个，从上往下试）",
		"遇到加密包时按顺序试。zip（含 AES）/ rar / 7z 都支持。", 66, &a.fPwList)
}

// tabHTTP 是 http/https 协议的参数，代理也放在这里（只对 HTTP 生效）。
func (a *app) tabHTTP(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "单任务连接数").FontSize(13)
			ui.Text(c, "多线程分段下载的并发连接，默认 16").FontSize(11).TextColor(t.TextMuted)
		})
		ui.TextInput(c, &a.fHttpConn).Width(90)
	})
	a.input(c, t, "User-Agent", "Mozilla/5.0", &a.fHttpUA)
	a.label(c, t, "有些站点按 UA 拒绝请求，可在这里伪装。")
	a.toggle(c, t, "使用服务器返回的文件时间", "用 Last-Modified 设置下载文件的 mtime", &a.fHttpCtime)

	ui.Divider(c)
	ui.Text(c, "代理").FontSize(14).Bold()
	a.toggle(c, t, "全部下载经代理", "所有 HTTP / HTTPS 下载都走下面的代理", &a.fProxyAll)
	a.toggle(c, t, "部分下载经代理", "只把网址里含下面关键字的下载交给代理，命中才走、不命中直连", &a.fProxyPartial)
	a.multiline(c, t, "关键字（一行一个，不区分大小写）", "github.com\npixiv.net", 90, &a.fProxyKeywords)
	a.label(c, t, "两个开关都关 = 不用代理；都打开时按「部分」算。只看网址里的子串，写 github 会连 raw.githubusercontent.com 一起代理。BT 永远直连，不受这里影响。")

	ui.Divider(c)
	a.toggle(c, t, "使用系统环境变量里的代理", "读 HTTP_PROXY / HTTPS_PROXY，忽略下面手填的地址", &a.fProxySystem)
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "代理类型").FontSize(13)
			ui.Text(c, "http / https / socks5").FontSize(11).TextColor(t.TextMuted)
		})
		ui.TextInput(c, &a.fProxyScheme).Placeholder("http").Width(120)
	})
	a.input(c, t, "代理地址", "127.0.0.1:7890", &a.fProxyHost)
	a.input(c, t, "用户名（可选）", "", &a.fProxyUser)
	a.input(c, t, "密码（可选）", "", &a.fProxyPass)
}

// tabBT 是 BitTorrent 参数。
func (a *app) tabBT(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "监听端口").FontSize(13)
			ui.Text(c, "0 = 每次随机挑一个空闲端口").FontSize(11).TextColor(t.TextMuted)
		})
		ui.TextInput(c, &a.fBtPort).Width(90)
	})
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "做种到分享率").FontSize(13)
			ui.Text(c, "上传量 / 下载量 达到这个值就停，0 = 不做种").FontSize(11).TextColor(t.TextMuted)
		})
		ui.TextInput(c, &a.fBtRatio).Width(90)
	})
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "做种时长（秒）").FontSize(13)
			ui.Text(c, "到时间就停，0 = 不限时（只看分享率）").FontSize(11).TextColor(t.TextMuted)
		})
		ui.TextInput(c, &a.fBtTime).Width(90)
	})
	a.toggle(c, t, "一直做种不自动停", "忽略上面的分享率与时长，下完继续挂着做种", &a.fBtKeep)
	ui.Divider(c)
	a.toggle(c, t, "做种自动清理任务", "下载完成后满足下面的条件就删掉任务记录，磁盘上的文件保留；后台每 10 分钟扫一次", &a.fSeedClean)
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "目标分享率").FontSize(13)
			ui.Text(c, "上传量 / 下载量 ≥ 该值就清理，0 = 不按分享率判断").FontSize(11).TextColor(t.TextMuted)
		})
		ui.TextInput(c, &a.fSeedRatio).Width(90)
	})
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "无上传清理（分钟）").FontSize(13)
			ui.Text(c, "连续这么多分钟没有上传字节就清理，0 = 不看空闲；要比 10 分钟明显大才有意义").FontSize(11).TextColor(t.TextMuted)
		})
		ui.TextInput(c, &a.fSeedIdle).Width(90)
	})
	a.toggle(c, t, "重启后自动续传", "gopeed 重启后把上次没下完的任务自动继续；手动暂停的任务不会被拉起", &a.fResumeOnStart)
	ui.Divider(c)
	a.multiline(c, t, "Tracker 列表（一行一个）", "udp://tracker.opentrackr.org:1337/announce", 90, &a.fBtTrackers)
}

// tabNotify 是 webhook 与脚本。
func (a *app) tabNotify(c *ui.Context, t *ui.Theme) {
	a.toggle(c, t, "启用 Webhook", "任务状态变化时 POST 到下面的地址", &a.fHookEnable)
	a.multiline(c, t, "Webhook 地址（一行一个）", "http://192.0.2.10:8080/hook", 90, &a.fHookURLs)
	ui.Divider(c)
	a.toggle(c, t, "启用脚本执行", "下载完成后调用指定脚本，路径须在服务器上真实存在", &a.fScriptEnable)
	a.multiline(c, t, "脚本路径（一行一个）", "/opt/gopeed/hooks/on-done.sh", 90, &a.fScriptPaths)
}

// tabConn 是客户端自己的连接设置（存在浏览器 localStorage 里，不上传服务器）。
func (a *app) tabConn(c *ui.Context, t *ui.Theme) {
	a.input(c, t, "服务器地址", defaultServer, &a.server)
	a.label(c, t, "留空则用当前页面地址（同源反代 /api）。")
	a.input(c, t, "API Token", "服务器没开鉴权就留空", &a.token)
	if a.srvInfo != "" {
		a.label(c, t, "服务器： "+a.srvInfo)
	}
}

// ---------- 小工具 ----------

// splitLines 把多行文本拆成去空白、去空行的切片。
func splitLines(s string) []string {
	out := []string{}
	for _, line := range strings.Split(s, "\n") {
		if v := strings.TrimSpace(line); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// atoiOr 解析整数，失败或为空时用默认值。
func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}

// atofOr 解析小数，失败或为空时用默认值。
func atofOr(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return def
}

// orInt 在 v 为 0 时返回 def（配置里 0 往往表示「未设置」）。
func orInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// trimFloat 去掉浮点数尾巴上的 0，让 5.000000 显示成 5。
func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func statusText(s string) string {
	switch s {
	case "ready":
		return "准备中"
	case "running":
		return "下载中"
	case "wait":
		return "排队中"
	case "pause":
		return "已暂停"
	case "error":
		return "出错"
	case "done":
		return "已完成"
	}
	return s
}

func statusColor(s string, t *ui.Theme) ui.Color {
	switch s {
	case "running", "wait", "ready":
		return t.Accent
	case "done":
		return ui.RGBA(52, 199, 89, 1)
	case "error":
		return t.Danger
	}
	return t.TextMuted
}

func human(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// protoColor 给协议徽标上色：BT 紫、HTTP 蓝，其余用弱化色。
func protoColor(proto string, t *ui.Theme) ui.Color {
	switch proto {
	case "bt":
		return ui.RGBA(140, 110, 245, 1)
	case "http":
		return ui.RGBA(70, 150, 245, 1)
	}
	return t.TextMuted
}

// kv 是详情里的一行「标签 + 值」，标签定宽让两列能对齐。
func (a *app) kv(c *ui.Context, t *ui.Theme, k, v string) {
	if v == "" {
		v = "—"
	}
	ui.Row(c).Gap(8).Children(func() {
		ui.Text(c, k).FontSize(12).TextColor(t.TextMuted).Width(76)
		ui.Text(c, v).FontSize(12).Grow(1).SingleLine()
	})
}

// kvWide 是占整行、允许折行的「标签 + 值」，给路径、hash、链接这类长字符串用。
func (a *app) kvWide(c *ui.Context, t *ui.Theme, k, v string) {
	ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
		ui.Text(c, k).FontSize(12).TextColor(t.TextMuted).Width(76)
		ui.Text(c, v).FontSize(12).Grow(1).MinWidth(0).MaxLines(2)
	})
}

// statNum 从 stats 里取一个数字：JSON 解出来的数字都是 float64。
func statNum(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	f, ok := m[key].(float64)
	return f, ok
}

func progressText(done, size int64) string {
	if size <= 0 {
		return human(done)
	}
	return fmt.Sprintf("%s / %s（%.1f%%）", human(done), human(size),
		float64(done)*100/float64(size))
}

func rateText(n int64) string {
	if n <= 0 {
		return "—"
	}
	return human(n) + "/s"
}

func fmtTime(s string) string {
	if s == "" {
		return "—"
	}
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts.Local().Format("2006-01-02 15:04:05")
	}
	return s
}

// taskHash 是 infoHash / 文件哈希，统一大写显示。
func taskHash(task *gTask) string {
	if task.Meta == nil || task.Meta.Res == nil {
		return ""
	}
	return strings.ToUpper(task.Meta.Res.Hash)
}

// peersText 显示「在线节点 / 总节点」，BT 只看这两个数。
func peersText(m map[string]any) string {
	act, _ := statNum(m, "activePeers")
	tot, ok := statNum(m, "totalPeers")
	if !ok {
		return ""
	}
	if act > 0 {
		return fmt.Sprintf("%d / %d", int(act), int(tot))
	}
	return fmt.Sprintf("%d", int(tot))
}

func seedRatioText(m map[string]any) string {
	r, ok := statNum(m, "seedRatio")
	if !ok {
		return ""
	}
	return fmt.Sprintf("%.2f", r)
}

func connText(m map[string]any) string {
	if m == nil {
		return ""
	}
	if arr, ok := m["connections"].([]any); ok {
		return fmt.Sprintf("%d", len(arr))
	}
	return ""
}

func yesNo(b bool) string {
	if b {
		return "支持"
	}
	return "不支持"
}

func taskRange(task *gTask) bool {
	return task.Meta != nil && task.Meta.Res != nil && task.Meta.Res.Range
}

// sourceText 是「来源」：BT 的种子是超长 base64，只给个说明，别糊满屏幕。
func sourceText(task *gTask) string {
	if task.Meta == nil || task.Meta.Req == nil {
		return ""
	}
	u := task.Meta.Req.URL
	if strings.HasPrefix(u, "data:") {
		return "种子文件（内嵌 base64）"
	}
	return u
}

// ---------- 入口 ----------

func main() {
	// 之前登录过就带上密钥，省得每次刷新都要重新输
	panelKey = loadStr("gopeed.panelkey")
	a := &app{server: resolveServer(), token: loadStr("gopeed.token")}
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{Title: "Gopeed", Content: ui.View(a.view)})
		go a.poll()
		go a.refresh(false)
		go a.primeConfig()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
