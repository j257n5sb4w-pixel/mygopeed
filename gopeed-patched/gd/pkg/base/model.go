package base

import (
	"fmt"
	"net/http"
	"net/url"
	syspath "path"
	"strings"
	"sync"
	"time"

	"github.com/GopeedLab/gopeed/pkg/util"
	"github.com/mattn/go-ieproxy"
	"golang.org/x/exp/slices"
)

// Request download request
type Request struct {
	URL   string `json:"url"`
	Extra any    `json:"extra"`
	// Labels is used to mark the download task
	Labels map[string]string `json:"labels"`
	// Proxy is special proxy config for request
	Proxy *RequestProxy `json:"proxy"`
	// SkipVerifyCert is the flag that skip verify cert
	SkipVerifyCert bool `json:"skipVerifyCert"`
}

func (r *Request) Validate() error {
	if r.URL == "" {
		return fmt.Errorf("invalid request url")
	}
	return nil
}

type RequestProxyMode string

const (
	// RequestProxyModeFollow follow setting proxy
	RequestProxyModeFollow RequestProxyMode = "follow"
	// RequestProxyModeNone not use proxy
	RequestProxyModeNone RequestProxyMode = "none"
	// RequestProxyModeCustom custom proxy
	RequestProxyModeCustom RequestProxyMode = "custom"
)

type RequestProxy struct {
	Mode   RequestProxyMode `json:"mode"`
	Scheme string           `json:"scheme"`
	Host   string           `json:"host"`
	Usr    string           `json:"usr"`
	Pwd    string           `json:"pwd"`
}

func (p *RequestProxy) ToHandler() func(r *http.Request) (*url.URL, error) {
	if p == nil || p.Mode != RequestProxyModeCustom {
		return nil
	}

	if p.Scheme == "" || p.Host == "" {
		return nil
	}

	return http.ProxyURL(util.BuildProxyUrl(p.Scheme, p.Host, p.Usr, p.Pwd))
}

// Resource download resource
type Resource struct {
	// if name is not empty, the resource is a folder and the name is the folder name
	Name string `json:"name"`
	Size int64  `json:"size"`
	// is support range download
	Range bool `json:"range"`
	// file list
	Files []*FileInfo `json:"files"`
	Hash  string      `json:"hash"`
}

func (r *Resource) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("invalid resource name")
	}
	if len(r.Files) == 0 {
		return fmt.Errorf("invalid resource files")
	}
	for _, file := range r.Files {
		if file.Name == "" {
			return fmt.Errorf("invalid resource file name")
		}
	}
	return nil
}

func (r *Resource) CalcSize(selectFiles []int) {
	var size int64
	for i, file := range r.Files {
		if len(selectFiles) == 0 || slices.Contains(selectFiles, i) {
			size += file.Size
		}
	}
	r.Size = size
}

type FileInfo struct {
	Name  string     `json:"name"`
	Path  string     `json:"path"`
	Size  int64      `json:"size"`
	Ctime *time.Time `json:"ctime"`

	Req *Request `json:"req"`
}

// Options for download
type Options struct {
	// Download file name
	Name string `json:"name"`
	// Download file path
	Path string `json:"path"`
	// Select file indexes to download
	SelectFiles []int `json:"selectFiles"`
	// Extra info for specific fetcher
	Extra any `json:"extra"`
	// Rename is the name the download result gets once the download is done.
	//
	// It is a bare file name (no directory separator). For a single file task the
	// original extension is kept when Rename has none; for a multi file task the
	// torrent folder is renamed. After a successful rename Name is updated to the
	// new base name, so task display and "delete with files" follow the new name.
	Rename string `json:"rename"`
}

func (o *Options) InitSelectFiles(fileSize int) {
	// if selectFiles is empty, select all files
	if len(o.SelectFiles) == 0 {
		o.SelectFiles = make([]int, fileSize)
		for i := range fileSize {
			o.SelectFiles[i] = i
		}
	}
}

func (o *Options) Clone() *Options {
	return util.DeepClone(o)
}

func ParseReqExtra[E any](req *Request) error {
	if req.Extra == nil {
		return nil
	}
	if _, ok := req.Extra.(*E); ok {
		return nil
	}
	var t E
	if err := util.MapToStruct(req.Extra, &t); err != nil {
		return err
	}
	req.Extra = &t
	return nil
}

func ParseOptExtra[E any](opts *Options) error {
	if opts.Extra == nil {
		return nil
	}
	if _, ok := opts.Extra.(*E); ok {
		return nil
	}
	var t E
	if err := util.MapToStruct(opts.Extra, &t); err != nil {
		return err
	}
	opts.Extra = &t
	return nil
}

type CreateTaskBatch struct {
	Reqs []*CreateTaskBatchItem `json:"reqs"`
	Opts *Options               `json:"opts"`
}

type CreateTaskBatchItem struct {
	Req  *Request `json:"req"`
	Opts *Options `json:"opts"`
}

// DownloaderStoreConfig is the config that can restore the downloader.
type DownloaderStoreConfig struct {
	FirstLoad bool `json:"-"` // FirstLoad is the flag that the config is first time init and not from store

	DownloadDir                string                 `json:"downloadDir"`    // DownloadDir is the default directory to save the downloaded files
	MaxRunning                 int                    `json:"maxRunning"`     // MaxRunning is the max running download count
	ProtocolConfig             map[string]any         `json:"protocolConfig"` // ProtocolConfig is special config for each protocol
	Extra                      map[string]any         `json:"extra"`
	Proxy                      *DownloaderProxyConfig `json:"proxy"`
	Webhook                    *WebhookConfig         `json:"webhook"`                    // Webhook is the webhook configuration
	Script                     *ScriptConfig          `json:"script"`                     // Script is the script execution configuration
	AutoTorrent                *AutoTorrentConfig     `json:"autoTorrent"`                // AutoTorrent is the auto torrent task creation configuration
	Archive                    *ArchiveConfig         `json:"archive"`                    // Archive is the archive extraction configuration
	AutoDeleteMissingFileTasks bool                   `json:"autoDeleteMissingFileTasks"` // AutoDeleteMissingFileTasks enables automatic deletion of tasks with missing files
	PathMappings               []PathMapping          `json:"pathMappings"`               // PathMappings rewrites incoming download directories to real on-disk directories
	SeedAutoRemove             *SeedAutoRemoveConfig  `json:"seedAutoRemove"`             // SeedAutoRemove drops finished BT tasks that have seeded enough
	ResumeOnStart              *bool                  `json:"resumeOnStart"`              // ResumeOnStart continues unfinished tasks after a restart
}

// SeedAutoRemoveConfig drops a finished BT task from the list once it has
// seeded enough. The task record is removed with force=false, so the
// downloaded files stay on disk. Ratio and IdleMinutes are independent
// conditions; 0 disables that condition, and Enable=false disables both.
type SeedAutoRemoveConfig struct {
	// Enable turns the whole feature on.
	Enable bool `json:"enable"`
	// Ratio removes the task once uploaded/downloaded reaches this value.
	Ratio float64 `json:"ratio"`
	// IdleMinutes removes the task after this many minutes without any new
	// uploaded bytes, i.e. nobody is downloading from us any more.
	IdleMinutes int64 `json:"idleMinutes"`
}

// PathMapping rewrites a download directory prefix.
//
// The caller (a panel, another service on the same network, ...) may use a path
// that only makes sense in its own world mount namespace, e.g. /media/anime. The
// downloader runs on a different machine/host mount, where the same place is
// /mnt/nas/media/anime. Mappings are applied longest-From-prefix first; the rest
// of the path is kept verbatim and unconditional ".." segments are rejected.
type PathMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (cfg *DownloaderStoreConfig) Init() *DownloaderStoreConfig {
	if cfg.MaxRunning == 0 {
		cfg.MaxRunning = 5
	}
	if cfg.ProtocolConfig == nil {
		cfg.ProtocolConfig = make(map[string]any)
	}
	if cfg.Proxy == nil {
		cfg.Proxy = &DownloaderProxyConfig{}
	}
	// Normalise the proxy mode: old configs only had Enable, new ones have a
	// three-way Mode. Keep both in sync so the panel and the JSON stay sane.
	if cfg.Proxy.EffectiveMode() == ProxyModeOff {
		cfg.Proxy.Mode = ProxyModeOff
		cfg.Proxy.Enable = false
	} else {
		cfg.Proxy.Mode = cfg.Proxy.EffectiveMode()
		cfg.Proxy.Enable = true
	}
	if cfg.Proxy.Keywords == nil {
		cfg.Proxy.Keywords = []string{}
	}
	if cfg.Webhook == nil {
		cfg.Webhook = &WebhookConfig{}
	}
	if cfg.Script == nil {
		cfg.Script = &ScriptConfig{}
	}
	if cfg.AutoTorrent == nil {
		cfg.AutoTorrent = &AutoTorrentConfig{
			Enable:              false,
			DeleteAfterDownload: false,
		}
	}
	if cfg.Archive == nil {
		cfg.Archive = &ArchiveConfig{
			AutoExtract:        false,
			DeleteAfterExtract: false,
		}
	}
	if cfg.PathMappings == nil {
		cfg.PathMappings = []PathMapping{}
	}
	if cfg.SeedAutoRemove == nil {
		cfg.SeedAutoRemove = &SeedAutoRemoveConfig{}
	}
	if cfg.ResumeOnStart == nil {
		// Default on: a crash or a systemd restart should not leave every
		// unfinished task sitting in the paused state.
		def := true
		cfg.ResumeOnStart = &def
	}
	return cfg
}

// MapPath rewrites a download directory through the configured path mappings.
//
// The mapping with the longest matching From prefix wins; the remainder of the
// path is appended to To verbatim. Paths that match no mapping are returned
// unchanged, so an empty configuration is always a no-op.
func (cfg *DownloaderStoreConfig) MapPath(p string) string {
	if cfg == nil || p == "" || len(cfg.PathMappings) == 0 {
		return p
	}
	clean := syspath.Clean(p)
	bestLen := -1
	var bestTo, bestRel string
	for _, m := range cfg.PathMappings {
		from := syspath.Clean(strings.TrimSpace(m.From))
		to := syspath.Clean(strings.TrimSpace(m.To))
		if from == "" || from == "." || to == "" || to == "." {
			continue
		}
		if clean != from && !strings.HasPrefix(clean, from+"/") {
			continue
		}
		if len(from) <= bestLen {
			continue
		}
		bestLen = len(from)
		bestTo = to
		bestRel = strings.TrimPrefix(strings.TrimPrefix(clean, from), "/")
	}
	if bestLen < 0 {
		return p
	}
	if bestRel == "" {
		return bestTo
	}
	return syspath.Join(bestTo, bestRel)
}

func (cfg *DownloaderStoreConfig) Merge(beforeCfg *DownloaderStoreConfig) *DownloaderStoreConfig {
	if beforeCfg == nil {
		return cfg
	}
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = beforeCfg.DownloadDir
	}
	if cfg.MaxRunning == 0 {
		cfg.MaxRunning = beforeCfg.MaxRunning
	}
	if cfg.ProtocolConfig == nil {
		cfg.ProtocolConfig = beforeCfg.ProtocolConfig
	}
	if cfg.Extra == nil {
		cfg.Extra = beforeCfg.Extra
	}
	if cfg.Proxy == nil {
		cfg.Proxy = beforeCfg.Proxy
	}
	if cfg.Webhook == nil {
		cfg.Webhook = beforeCfg.Webhook
	}
	if cfg.Script == nil {
		cfg.Script = beforeCfg.Script
	}
	if cfg.AutoTorrent == nil {
		cfg.AutoTorrent = beforeCfg.AutoTorrent
	}
	if cfg.Archive == nil {
		cfg.Archive = beforeCfg.Archive
	}
	if cfg.PathMappings == nil {
		cfg.PathMappings = beforeCfg.PathMappings
	}
	return cfg
}

// WebhookConfig is the webhook configuration
type WebhookConfig struct {
	Enable bool     `json:"enable"` // Enable is the flag to enable/disable webhooks
	URLs   []string `json:"urls"`   // URLs is the list of webhook URLs
}

// ScriptConfig is the script execution configuration
type ScriptConfig struct {
	Enable bool     `json:"enable"` // Enable is the flag to enable/disable script execution
	Paths  []string `json:"paths"`  // Paths is the list of script paths to execute
}

// AutoTorrentConfig is the auto torrent task creation configuration
type AutoTorrentConfig struct {
	Enable              bool `json:"enable"`              // Enable enables automatic BT task creation when downloading .torrent files
	DeleteAfterDownload bool `json:"deleteAfterDownload"` // DeleteAfterDownload deletes the .torrent file after BT task creation
}

// ArchiveConfig is the archive extraction configuration
type ArchiveConfig struct {
	AutoExtract        bool `json:"autoExtract"`        // AutoExtract enables automatic extraction of archives after download
	DeleteAfterExtract bool `json:"deleteAfterExtract"` // DeleteAfterExtract deletes the archive after successful extraction
	// Passwords is a list of candidate passwords for encrypted archives.
	// They are tried from top to bottom until one works.
	Passwords []string `json:"passwords"`
}

type DownloaderProxyConfig struct {
	Enable bool `json:"enable"`
	// Mode decides which downloads go through the proxy: "off", "all" or
	// "partial". An empty value means "derive it from Enable", so configs
	// written by older versions keep working.
	//
	// The proxy only ever applies to HTTP downloads. BT always
	// connect directly (BT is a peer-to-peer swarm, a single HTTP proxy in
	// front of it only breaks trackers and peer discovery).
	Mode string `json:"mode"`
	// Keywords is only used by Mode=partial: an HTTP request whose URL
	// contains any of these strings (case-insensitive) is sent through the
	// proxy, every other request goes direct. Empty list means nothing is
	// proxied.
	Keywords []string `json:"keywords"`
	// System is the flag that use system proxy
	System bool   `json:"system"`
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
	Usr    string `json:"usr"`
	Pwd    string `json:"pwd"`
}

// Proxy modes.
const (
	ProxyModeOff     = "off"
	ProxyModeAll     = "all"
	ProxyModePartial = "partial"
)

// EffectiveMode returns Mode, falling back to Enable for old configs.
func (cfg *DownloaderProxyConfig) EffectiveMode() string {
	if cfg == nil {
		return ProxyModeOff
	}
	switch cfg.Mode {
	case ProxyModeAll, ProxyModePartial, ProxyModeOff:
		return cfg.Mode
	}
	if cfg.Enable {
		return ProxyModeAll
	}
	return ProxyModeOff
}

// MatchURL reports whether rawURL should be proxied in partial mode.
func (cfg *DownloaderProxyConfig) MatchURL(rawURL string) bool {
	if cfg == nil {
		return false
	}
	lower := strings.ToLower(rawURL)
	for _, kw := range cfg.Keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw == "" {
			continue
		}
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// resolveProxy returns the proxy URL for a single request, ignoring the mode.
func (cfg *DownloaderProxyConfig) resolveProxy() (*url.URL, error) {
	if cfg.System {
		safeProxyReloadConf()
		handler := ieproxy.GetProxyFunc()
		if handler == nil {
			return nil, nil
		}
		return handler(&http.Request{URL: &url.URL{Scheme: "http", Host: "example.com"}})
	}
	if cfg.Scheme == "" || cfg.Host == "" {
		return nil, nil
	}
	return util.BuildProxyUrl(cfg.Scheme, cfg.Host, cfg.Usr, cfg.Pwd), nil
}

// Resolve decides per request whether the given URL goes through the proxy.
// It is the entry point used by the HTTP fetcher, so changing the proxy
// settings takes effect on running tasks without rebuilding their clients.
func (cfg *DownloaderProxyConfig) Resolve(rawURL string) (*url.URL, error) {
	switch cfg.EffectiveMode() {
	case ProxyModeAll:
		return cfg.resolveProxy()
	case ProxyModePartial:
		if !cfg.MatchURL(rawURL) {
			return nil, nil
		}
		return cfg.resolveProxy()
	}
	return nil, nil
}

func (cfg *DownloaderProxyConfig) ToHandler() func(r *http.Request) (*url.URL, error) {
	if cfg == nil || cfg.EffectiveMode() == ProxyModeOff {
		return nil
	}
	if cfg.System {
		safeProxyReloadConf()
		return ieproxy.GetProxyFunc()
	}
	if cfg.Scheme == "" || cfg.Host == "" {
		return nil
	}
	return http.ProxyURL(util.BuildProxyUrl(cfg.Scheme, cfg.Host, cfg.Usr, cfg.Pwd))
}

// ToUrl returns the proxy url, just for git clone
func (cfg *DownloaderProxyConfig) ToUrl() *url.URL {
	// git clone（扩展安装）只认 all 模式：partial 的关键字表是给下载用的。
	if cfg == nil || cfg.EffectiveMode() != ProxyModeAll {
		return nil
	}
	if cfg.System {
		safeProxyReloadConf()
		static := ieproxy.GetConf().Static
		if static.Active && len(static.Protocols) > 0 {
			// If only one protocol, use it
			if len(static.Protocols) == 1 {
				for _, v := range static.Protocols {
					return parseUrlSafe(v)
				}
			}
			// Check https
			if v, ok := static.Protocols["https"]; ok {
				return parseUrlSafe(v)
			}
			// Check http
			if v, ok := static.Protocols["http"]; ok {
				return parseUrlSafe(v)
			}
		}
		return nil
	}
	if cfg.Scheme == "" || cfg.Host == "" {
		return nil
	}
	return util.BuildProxyUrl(cfg.Scheme, cfg.Host, cfg.Usr, cfg.Pwd)
}

var prcLock sync.Mutex

func safeProxyReloadConf() {
	prcLock.Lock()
	defer prcLock.Unlock()

	ieproxy.ReloadConf()
}

func parseUrlSafe(rawUrl string) *url.URL {
	u, err := url.Parse(rawUrl)
	if err != nil {
		return nil
	}
	return u
}
