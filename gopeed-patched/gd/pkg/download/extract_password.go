package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/mholt/archives"
	yzip "github.com/yeka/zip"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// candidatePasswords 合并「任务自带的密码」和「全局密码表」，去掉空串与重复项。
// 任务自带的密码排在最前面，因为它最可能是对的。
func candidatePasswords(taskPassword string, list []string) []string {
	seen := make(map[string]struct{}, len(list)+1)
	out := make([]string, 0, len(list)+1)
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	add(taskPassword)
	for _, p := range list {
		add(p)
	}
	return out
}

// isZipPath 只看扩展名判断是不是 zip（含分卷 .zip.001）。
func isZipPath(p string) bool {
	lower := strings.ToLower(p)
	return strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".zip.001")
}

// probeZipPassword 只读不写地验证 zip 密码。
// 解压目标目录往往就是压缩包所在的目录（gopeed 就是这么设计的），
// 所以绝不能靠「试错了就把目录清掉」来回滚 —— 那会把压缩包本身删了。
// 验证一遍再真正解压，是一次性、无副作用的做法。
func probeZipPassword(archivePath, password string) error {
	r, err := yzip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		f.SetPassword(password)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		buf := make([]byte, 512)
		_, readErr := io.ReadFull(rc, buf)
		rc.Close()
		// 文件比 512 字节短是正常情况，只有「真的读出错」才算密码不对
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		return nil
	}
	return errors.New("zip 里没有可解压的文件")
}

// errStopProbe 用来在读通一个文件后提前结束试读，避免为了验密码把整个包解一遍。
var errStopProbe = errors.New("probe ok")

// readSome 从 r 里读一点内容，用来触发解密/校验；文件比 n 字节短不算错。
func readSome(r io.Reader, n int) error {
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// probeArchivePassword 用 mholt/archives 只读不写地验证 rar / 7z 等格式的密码。
func probeArchivePassword(archivePath, password string) error {
	info, err := openArchive(archivePath, password)
	if err != nil {
		return err
	}
	defer info.file.Close()

	// got 表示至少有一个文件被成功读出内容 —— 这说明密码是对的。
	// 之所以不用 errors.Is 判断「提前结束」的哨兵错误，是因为各解压库可能把
	// handler 返回的错误包一层，靠哨兵判断会误判成密码错误。
	got := false
	handler := func(ctx context.Context, fileInfo archives.FileInfo) error {
		if fileInfo.IsDir() {
			return nil
		}
		rc, err := fileInfo.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		if err := readSome(rc, 512); err != nil {
			return err
		}
		got = true
		return errStopProbe
	}

	switch f := info.format.(type) {
	case archives.Extractor:
		err = f.Extract(context.Background(), info.input, handler)
	case archives.Decompressor:
		rc, oerr := f.OpenReader(info.input)
		if oerr != nil {
			return oerr
		}
		defer rc.Close()
		if rerr := readSome(rc, 512); rerr != nil {
			return rerr
		}
		return nil
	case archives.Archiver:
		if ext, ok := f.(archives.Extractor); ok {
			if seeker, ok := info.input.(io.Seeker); ok {
				_, _ = seeker.Seek(0, io.SeekStart)
			}
			err = ext.Extract(context.Background(),
				io.NewSectionReader(info.file, 0, info.stat.Size()), handler)
		}
	}
	if got {
		return nil
	}
	return err
}

func probePassword(archivePath, password string) error {
	if isZipPath(archivePath) {
		return probeZipPassword(archivePath, password)
	}
	return probeArchivePassword(archivePath, password)
}

// extractArchiveWithPasswords 先逐个验证密码（只读），命中后再真正解压一次。
func extractArchiveWithPasswords(archivePath, destDir string, passwords []string, cb ExtractProgressCallback) error {
	if len(passwords) == 0 {
		return extractArchive(archivePath, destDir, "", cb)
	}

	// ZIP 单独处理：标准库（archive/zip）根本没有解密实现，
	// 所以先看中央目录里的加密标志位，没加密就走原来的路径，
	// 加密了才退到 yeka/zip 逐个密码试。
	if isZipPath(archivePath) {
		encrypted, err := zipIsEncrypted(archivePath)
		if err != nil || !encrypted {
			return extractArchive(archivePath, destDir, "", cb)
		}
		for _, pw := range passwords {
			if probeZipPassword(archivePath, pw) == nil {
				return extractZipWithPassword(archivePath, destDir, pw, cb)
			}
		}
		return fmt.Errorf("密码表里没有能解开这个压缩包的密码（共试了 %d 个）", len(passwords))
	}

	var last error
	for _, pw := range passwords {
		if err := probeArchivePassword(archivePath, pw); err != nil {
			last = err
			continue
		}
		return extractArchive(archivePath, destDir, pw, cb)
	}
	if last == nil {
		last = errors.New("没有可用的解压密码")
	}
	return fmt.Errorf("密码表里没有能解开这个压缩包的密码：%w", last)
}

// extractMultiPartArchiveWithPasswords 是多卷版本，逻辑同上（只验第一个分卷）。
func extractMultiPartArchiveWithPasswords(firstPartPath, destDir string, passwords []string, cb ExtractProgressCallback) error {
	if len(passwords) == 0 {
		return extractMultiPartArchive(firstPartPath, destDir, "", cb)
	}
	var last error
	for _, pw := range passwords {
		if err := probePassword(firstPartPath, pw); err != nil {
			last = err
			continue
		}
		return extractMultiPartArchive(firstPartPath, destDir, pw, cb)
	}
	if last == nil {
		last = errors.New("没有可用的解压密码")
	}
	return fmt.Errorf("密码表里没有能解开这个压缩包的密码：%w", last)
}

// extractZipWithPassword 用 yeka/zip 解带密码的 zip（支持 ZipCrypto 与 AES）。
func extractZipWithPassword(archivePath, destDir, password string, cb ExtractProgressCallback) error {
	r, err := yzip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	total := 0
	for _, f := range r.File {
		if !f.FileInfo().IsDir() {
			total++
		}
	}

	extracted := 0
	for _, f := range r.File {
		name := f.Name
		// zip 里的文件名如果是 GBK（没打 UTF-8 标志），Go 会原样给出非法 UTF-8 字节
		if !utf8.ValidString(name) {
			if decoded, derr := simplifiedchinese.GB18030.NewDecoder().String(name); derr == nil {
				name = decoded
			}
		}
		// 挡住 ../ 这种越界路径
		clean := filepath.Clean(name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		dest := filepath.Join(destDir, clean)

		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(dest, f.Mode())
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}

		f.SetPassword(password)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(dest)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		_ = os.Chmod(dest, f.Mode())

		extracted++
		if cb != nil && total > 0 {
			progress := extracted * 100 / total
			if progress > 100 {
				progress = 100
			}
			cb(extracted, total, progress)
		}
	}
	if extracted == 0 {
		return errors.New("zip 里没有可解压的文件")
	}
	return nil
}

// archivePasswords 取全局的「自动解压密码表」，配置缺字段时返回空。
func (d *Downloader) archivePasswords() []string {
	if d.cfg == nil || d.cfg.DownloaderStoreConfig == nil {
		return nil
	}
	if d.cfg.DownloaderStoreConfig.Archive == nil {
		return nil
	}
	return d.cfg.DownloaderStoreConfig.Archive.Passwords
}

// zipIsEncrypted 看中央目录里的加密标志位，判断这个 zip 是否加密。
// 比「试着解一遍看失不失败」可靠：未加密的 zip 也能正常走原来的解压路径
// （保留 GB18030 文件名处理），不会因为多传了个密码就被当成要解密。
func zipIsEncrypted(archivePath string) (bool, error) {
	r, err := yzip.OpenReader(archivePath)
	if err != nil {
		return false, err
	}
	defer r.Close()
	for _, f := range r.File {
		if !f.FileInfo().IsDir() && f.IsEncrypted() {
			return true, nil
		}
	}
	return false, nil
}
