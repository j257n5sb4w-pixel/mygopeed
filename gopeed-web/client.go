package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// panelKey 是面板访问密钥（base64(用户名:密码)）。Caddy 在 /api/* 上校验
// 请求头 X-Panel-Key，对不上就回 401；没设登录时这个变量是空串。
//
// 用自定义头而不是 HTTP Basic，是因为 Basic 的 401 会带上 WWW-Authenticate，
// 浏览器会弹自己的原生登录框，和我们的登录页打架。
var panelKey string

// errUnauthorized 表示面板要登录（Caddy 拦下的 401）。
var errUnauthorized = errors.New("unauthorized")

// ---------- gopeed REST 的同构类型 ----------

type gResult struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type gRequest struct {
	URL string `json:"url"`
}

type gOptions struct {
	Path        string `json:"path,omitempty"`
	SelectFiles []int  `json:"selectFiles,omitempty"`
}

type gFileInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type gResource struct {
	Name  string      `json:"name"`
	Size  int64       `json:"size"`
	Range bool        `json:"range"`
	Files []gFileInfo `json:"files"`
	Hash  string      `json:"hash"`
}

type gMeta struct {
	Req  *gRequest  `json:"req"`
	Res  *gResource `json:"res"`
	Opts *gOptions  `json:"opts"`
}

type gProgress struct {
	Used            int64  `json:"used"`
	Speed           int64  `json:"speed"`
	Downloaded      int64  `json:"downloaded"`
	UploadSpeed     int64  `json:"uploadSpeed"`
	Uploaded        int64  `json:"uploaded"`
	ExtractStatus   string `json:"extractStatus"`
	ExtractProgress int    `json:"extractProgress"`
}

type gTask struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Protocol  string     `json:"protocol"`
	Meta      *gMeta     `json:"meta"`
	Status    string     `json:"status"`
	Progress  *gProgress `json:"progress"`
	IsCreated bool       `json:"isCreated"`
	// CreatedAt / UpdatedAt 是 RFC3339 时间串，详情弹窗里显示创建时间用。
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// gConfig 对应 gopeed 的 base.DownloaderStoreConfig。
//
// 注意：PUT /api/v1/config 是**整份替换**（downloader.go:952 直接把入参赋给
// d.cfg.DownloaderStoreConfig 并落库），不是按键合并。漏传任何字段都会把它
// 归零，所以保存时必须带上完整结构。
type gConfig struct {
	DownloadDir                string         `json:"downloadDir"`
	MaxRunning                 int            `json:"maxRunning"`
	ProtocolConfig             map[string]any `json:"protocolConfig"`
	Extra                      map[string]any `json:"extra"`
	Proxy                      *gProxy        `json:"proxy"`
	Webhook                    *gWebhook      `json:"webhook"`
	Script                     *gScript       `json:"script"`
	AutoTorrent                *gAutoTorrent  `json:"autoTorrent"`
	Archive                    *gArchive      `json:"archive"`
	AutoDeleteMissingFileTasks bool           `json:"autoDeleteMissingFileTasks"`
	// 下面两个是本机补丁加的字段（不是官方 1.9.3 的）。这里必须声明，
	// 否则 PUT 整份配置时会把它们抹掉。
	SeedAutoRemove *gSeedAutoRemove `json:"seedAutoRemove"`
	ResumeOnStart  *bool            `json:"resumeOnStart"`
	// pathMappings 决定「调用方给的目录 → 本机真实目录」，面板不编辑它，
	// 但同样必须原样带回去，否则保存设置一次映射就没了。
	PathMappings []gPathMapping `json:"pathMappings"`
}

// gPathMapping 是 gopeed 侧的前缀映射，例如 /media → /mnt/nas/media。
type gPathMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// gSeedAutoRemove 是「做种自动清理」：BT 任务正常下载完成后，分享率达标、
// 或连续一段时间没有上传字节，就删掉任务记录（force=false，磁盘文件不动）。
type gSeedAutoRemove struct {
	Enable      bool    `json:"enable"`
	Ratio       float64 `json:"ratio"`
	IdleMinutes int     `json:"idleMinutes"`
}

type gProxy struct {
	Enable bool `json:"enable"`
	// Mode 决定代理的生效范围："off" / "all" / "partial"。
	// 老配置只写了 enable，读的时候按 enable 推。
	Mode string `json:"mode"`
	// Keywords 只在 partial 模式下用：HTTP 请求的网址里含任意一行关键字
	// （不区分大小写）就走代理，其余直连。BT 永远不走代理。
	Keywords []string `json:"keywords"`
	System   bool     `json:"system"`
	Scheme   string   `json:"scheme"`
	Host     string   `json:"host"`
	Usr      string   `json:"usr"`
	Pwd      string   `json:"pwd"`
}

type gWebhook struct {
	Enable bool     `json:"enable"`
	URLs   []string `json:"urls"`
}

type gScript struct {
	Enable bool     `json:"enable"`
	Paths  []string `json:"paths"`
}

type gAutoTorrent struct {
	Enable              bool `json:"enable"`
	DeleteAfterDownload bool `json:"deleteAfterDownload"`
}

type gArchive struct {
	AutoExtract        bool `json:"autoExtract"`
	DeleteAfterExtract bool `json:"deleteAfterExtract"`
	// Passwords 是加密压缩包的候选密码，按顺序从上往下试。
	Passwords []string `json:"passwords"`
}

// 各协议的配置（internal/protocol/{http,bt}/config.go）。
type gHTTPConf struct {
	UserAgent      string `json:"userAgent"`
	Connections    int    `json:"connections"`
	UseServerCtime bool   `json:"useServerCtime"`
}

type gBTConf struct {
	ListenPort int      `json:"listenPort"`
	Trackers   []string `json:"trackers"`
	SeedKeep   bool     `json:"seedKeep"`
	SeedRatio  float64  `json:"seedRatio"`
	SeedTime   int64    `json:"seedTime"`
}

// proto 把 protocolConfig["name"] 解到 v；不存在或类型不符时返回 false。
func (c *gConfig) proto(name string, v any) bool {
	if c.ProtocolConfig == nil {
		return false
	}
	raw, ok := c.ProtocolConfig[name]
	if !ok || raw == nil {
		return false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

func (c *gConfig) httpConf() gHTTPConf {
	v := gHTTPConf{Connections: 16}
	c.proto("http", &v)
	if v.Connections <= 0 {
		v.Connections = 16
	}
	return v
}

func (c *gConfig) btConf() gBTConf {
	v := gBTConf{SeedRatio: 1, SeedTime: 7200}
	c.proto("bt", &v)
	return v
}

// setProto 写回一条协议配置，其余协议保持不变。
func (c *gConfig) setProto(name string, v any) {
	if c.ProtocolConfig == nil {
		c.ProtocolConfig = map[string]any{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	var m map[string]any
	if json.Unmarshal(b, &m) == nil {
		c.ProtocolConfig[name] = m
	}
}

// ---------- 客户端 ----------

type client struct {
	base  string
	token string
	http  *http.Client
	slow  *http.Client
}

func newClient(base, token string) *client {
	return &client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 20 * time.Second},
		slow:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *client) doWith(hc *http.Client, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("X-Api-Token", c.token)
	}
	if panelKey != "" {
		req.Header.Set("X-Panel-Key", panelKey)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errUnauthorized
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r gResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("HTTP %d：响应不是合法 JSON", resp.StatusCode)
	}
	if r.Code != 0 {
		return nil, fmt.Errorf("%s", r.Msg)
	}
	return r.Data, nil
}

func (c *client) do(method, path string, body any) (json.RawMessage, error) {
	return c.doWith(c.http, method, path, body)
}

func (c *client) info() (string, error) {
	raw, err := c.do("GET", "/api/v1/info", nil)
	if err != nil {
		return "", err
	}
	var m struct {
		OS      string `json:"os"`
		Arch    string `json:"arch"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s · v%s", m.OS, m.Arch, m.Version), nil
}

func (c *client) tasks() ([]gTask, error) {
	raw, err := c.do("GET", "/api/v1/tasks", nil)
	if err != nil {
		return nil, err
	}
	var ts []gTask
	return ts, json.Unmarshal(raw, &ts)
}

// task 取单条任务：列表接口不带的字段（createdAt、完整 files）这里能拿到。
func (c *client) task(id string) (*gTask, error) {
	raw, err := c.do("GET", "/api/v1/tasks/"+id, nil)
	if err != nil {
		return nil, err
	}
	var t gTask
	return &t, json.Unmarshal(raw, &t)
}

func (c *client) stats(id string) (map[string]any, error) {
	raw, err := c.do("GET", "/api/v1/tasks/"+id+"/stats", nil)
	if err != nil {
		return nil, err
	}
	var v map[string]any
	return v, json.Unmarshal(raw, &v)
}

func (c *client) create(req *gRequest, opts *gOptions) (string, error) {
	raw, err := c.do("POST", "/api/v1/tasks", map[string]any{"req": req, "opts": opts})
	if err != nil {
		return "", err
	}
	var m struct {
		ID string `json:"id"`
	}
	json.Unmarshal(raw, &m)
	return m.ID, nil
}

func (c *client) action(id, act string) error {
	_, err := c.do("PUT", "/api/v1/tasks/"+id+"/"+act, nil)
	if err != nil && strings.Contains(err.Error(), "task not found") {
		return nil // 已经是该状态，语义上无需操作
	}
	return err
}

func (c *client) del(id string, withFiles bool) error {
	q := ""
	if withFiles {
		q = "?force=true"
	}
	_, err := c.do("DELETE", "/api/v1/tasks/"+id+q, nil)
	return err
}

func (c *client) config() (*gConfig, error) {
	raw, err := c.do("GET", "/api/v1/config", nil)
	if err != nil {
		return nil, err
	}
	cfg := &gConfig{}
	return cfg, json.Unmarshal(raw, cfg)
}

// putConfig 整份提交配置（见 gConfig 注释：这是替换语义，不能只传改动项）。
func (c *client) putConfig(cfg *gConfig) error {
	_, err := c.do("PUT", "/api/v1/config", cfg)
	return err
}
