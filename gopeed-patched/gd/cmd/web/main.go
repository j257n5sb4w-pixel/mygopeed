//go:build web
// +build web

package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/GopeedLab/gopeed/cmd"
	"github.com/GopeedLab/gopeed/pkg/rest/model"
)

//go:embed all:dist
var dist embed.FS

func main() {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}

	args := parse()
	var webAuth *model.WebAuth
	if isNotBlank(args.Username) && isNotBlank(args.Password) {
		webAuth = &model.WebAuth{
			Username: *args.Username,
			Password: *args.Password,
		}
	}

	var storageDir string
	if args.StorageDir != nil && *args.StorageDir != "" {
		storageDir = *args.StorageDir
	} else {
		exe, err := os.Executable()
		if err != nil {
			panic(err)
		}
		storageDir = filepath.Join(filepath.Dir(exe), "storage")
	}

	address := fmt.Sprintf("%s:%d", *args.Address, *args.Port)

	cfg := &model.StartConfig{
		Network:           "tcp",
		Address:           address,
		Storage:           model.StorageBolt,
		StorageDir:        storageDir,
		WhiteDownloadDirs: args.WhiteDownloadDirs,
		ApiToken:          *args.ApiToken,
		DownloadConfig:    args.DownloadConfig,
		ProductionMode:    true,
		WebEnable:         true,
		WebFS:             sub,
		WebAuth:           webAuth,
	}

	// 面板与 REST API 共用同一个端口，所以这里把访问入口打出来，方便直接点开。
	fmt.Printf("%s\n", strings.Repeat("=", 60))
	if webAuth == nil {
		fmt.Println("  [!] 未设置 web 密码，面板与 API 均无鉴权，请勿直接暴露到公网")
	} else {
		fmt.Printf("  面板与 API 共用端口 : http://%s\n", address)
		fmt.Printf("  面板登录用户名      : %s\n", webAuth.Username)
	}
	fmt.Printf("%s\n", strings.Repeat("=", 60))

	cmd.Start(cfg)
}

func isNotBlank(str *string) bool {
	return str != nil && *str != ""
}
