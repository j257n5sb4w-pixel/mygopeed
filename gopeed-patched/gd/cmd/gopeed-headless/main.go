package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GopeedLab/gopeed/cmd"
	"github.com/GopeedLab/gopeed/pkg/base"
	"github.com/GopeedLab/gopeed/pkg/rest/model"
)

func main() {
	var (
		network  = flag.String("network", "tcp", "listen network: tcp | unix")
		address  = flag.String("A", "0.0.0.0", "bind address")
		port     = flag.Int("P", 9999, "bind port (tcp only)")
		apiToken = flag.String("T", "", "API token; empty disables auth")
		storage  = flag.String("d", "", "storage dir (default: <exe dir>/storage)")
		white    = flag.String("white-download-dirs", "", "comma separated download dir allowlist")
		dlDir    = flag.String("download-dir", "", "default download dir")
		maxRun   = flag.Int("max-running", 0, "max concurrent running tasks (0 keeps stored value)")
		confFile = flag.String("c", "", "JSON file with a base.DownloaderStoreConfig used as the initial config")
		showVer  = flag.Bool("v", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("gopeed %s (headless)\n", base.Version)
		return
	}

	var storageDir string
	if *storage != "" {
		storageDir = *storage
	} else {
		exe, err := os.Executable()
		if err != nil {
			fatal(err)
		}
		storageDir = filepath.Join(filepath.Dir(exe), "storage")
	}
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		fatal(err)
	}

	var whiteDirs []string
	if *white != "" {
		for _, d := range strings.Split(*white, ",") {
			if d = strings.TrimSpace(d); d != "" {
				whiteDirs = append(whiteDirs, d)
			}
		}
	}

	var dlCfg *base.DownloaderStoreConfig
	if *confFile != "" {
		raw, err := os.ReadFile(*confFile)
		if err != nil {
			fatal(err)
		}
		dlCfg = &base.DownloaderStoreConfig{}
		if err := json.Unmarshal(raw, dlCfg); err != nil {
			fatal(fmt.Errorf("parse %s: %w", *confFile, err))
		}
	}
	if dlCfg == nil {
		dlCfg = &base.DownloaderStoreConfig{}
	}
	if *dlDir != "" {
		dlCfg.DownloadDir = *dlDir
	}
	if *maxRun > 0 {
		dlCfg.MaxRunning = *maxRun
	}

	bind := *address
	if *network == "tcp" {
		bind = fmt.Sprintf("%s:%d", *address, *port)
	}

	cfg := &model.StartConfig{
		Network:           *network,
		Address:           bind,
		Storage:           model.StorageBolt,
		StorageDir:        storageDir,
		WhiteDownloadDirs: whiteDirs,
		ApiToken:          *apiToken,
		DownloadConfig:    dlCfg,
		ProductionMode:    true,
		// WebEnable stays false: this build embeds no web UI and serves the
		// REST API only. That keeps cmd/web's //go:embed dist/* requirement
		// out of the picture, so no Flutter artifacts are needed to build.
		WebEnable: false,
	}
	cmd.Start(cfg)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gopeed:", err)
	os.Exit(1)
}
