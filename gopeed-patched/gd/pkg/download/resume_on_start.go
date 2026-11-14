package download

import (
	"time"

	"github.com/GopeedLab/gopeed/pkg/base"
)

// shouldResumeOnStart reports whether unfinished tasks found at startup should
// be continued automatically.
func shouldResumeOnStart(cfg *base.DownloaderStoreConfig) bool {
	if cfg == nil {
		return true
	}
	// Missing key (old config) means "on".
	return cfg.ResumeOnStart == nil || *cfg.ResumeOnStart
}

// startResumeOnStart continues the tasks that were running (or queued) when the
// process died. They were forced into the pause state while loading, so without
// this every restart leaves the user with a pile of paused tasks.
func (d *Downloader) startResumeOnStart(ids []string) {
	if len(ids) == 0 {
		return
	}
	d.Logger.Info().Msgf("resume on start: found %d unfinished tasks", len(ids))
	go func() {
		// Give the fetchers a moment to settle before touching them.
		time.Sleep(2 * time.Second)
		if d.closed.Load() {
			return
		}
		if !shouldResumeOnStart(d.cfg.DownloaderStoreConfig) {
			d.Logger.Info().Msg("resume on start: disabled by config, skip")
			return
		}
		resumed := 0
		for _, id := range ids {
			if d.GetTask(id) == nil {
				continue
			}
			if err := d.Continue(&TaskFilter{IDs: []string{id}}); err != nil {
				d.Logger.Error().Err(err).Msgf("resume on start: continue task %s failed", id)
				continue
			}
			resumed++
		}
		d.Logger.Info().Msgf("resume on start: 已恢复 %d 个未完成任务", resumed)
	}()
}
