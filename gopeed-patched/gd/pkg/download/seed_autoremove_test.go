package download

import (
	"testing"
	"time"

	"github.com/GopeedLab/gopeed/pkg/base"
)

func TestSeedAutoRemoveReason(t *testing.T) {
	on := &base.SeedAutoRemoveConfig{Enable: true, Ratio: 2, IdleMinutes: 30}
	off := &base.SeedAutoRemoveConfig{Enable: false, Ratio: 2, IdleMinutes: 30}

	tests := []struct {
		name       string
		cfg        *base.SeedAutoRemoveConfig
		downloaded int64
		uploaded   int64
		idle       time.Duration
		want       string
	}{
		{"disabled", off, 100, 1000, 10 * time.Hour, ""},
		{"nil config", nil, 100, 1000, 10 * time.Hour, ""},
		{"nothing reached", on, 100, 100, time.Minute, ""},
		{"ratio reached", on, 100, 200, time.Minute, "ratio"},
		{"ratio above", on, 100, 500, time.Minute, "ratio"},
		{"idle reached", on, 100, 100, 30 * time.Minute, "idle"},
		{"idle below", on, 100, 100, 29 * time.Minute, ""},
		{"both reached prefers ratio", on, 100, 300, time.Hour, "ratio"},
		{"zero ratio means ignore", &base.SeedAutoRemoveConfig{Enable: true, Ratio: 0, IdleMinutes: 30}, 100, 100000, time.Minute, ""},
		{"zero idle means ignore", &base.SeedAutoRemoveConfig{Enable: true, Ratio: 2, IdleMinutes: 0}, 100, 100, time.Hour, ""},
		{"zero ratio and idle never fires", &base.SeedAutoRemoveConfig{Enable: true}, 100, 100, time.Hour, ""},
		{"downloaded zero skips ratio", on, 0, 1000, time.Minute, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := seedAutoRemoveReason(tt.cfg, tt.downloaded, tt.uploaded, tt.idle); got != tt.want {
				t.Fatalf("seedAutoRemoveReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShouldResumeOnStart(t *testing.T) {
	yes, no := true, false
	if !shouldResumeOnStart(nil) {
		t.Fatal("nil config should resume")
	}
	if !shouldResumeOnStart(&base.DownloaderStoreConfig{}) {
		t.Fatal("missing key should resume")
	}
	if !shouldResumeOnStart(&base.DownloaderStoreConfig{ResumeOnStart: &yes}) {
		t.Fatal("explicit true should resume")
	}
	if shouldResumeOnStart(&base.DownloaderStoreConfig{ResumeOnStart: &no}) {
		t.Fatal("explicit false must not resume")
	}
}

func TestStoreConfigSeedDefaults(t *testing.T) {
	cfg := (&base.DownloaderStoreConfig{}).Init()
	if cfg.SeedAutoRemove == nil {
		t.Fatal("SeedAutoRemove should be initialised")
	}
	if cfg.SeedAutoRemove.Enable {
		t.Fatal("SeedAutoRemove should default to disabled")
	}
	if cfg.ResumeOnStart == nil || !*cfg.ResumeOnStart {
		t.Fatal("ResumeOnStart should default to true")
	}
}
