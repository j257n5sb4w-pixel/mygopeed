package download

import (
	"time"

	"github.com/GopeedLab/gopeed/pkg/base"
)

// seedAutoRemoveInterval is how often finished BT tasks are inspected. It is a
// variable so the test suite can shrink it.
var seedAutoRemoveInterval = 10 * time.Minute

// seedWatch remembers, per task, how long its uploaded byte counter has been
// standing still. It is kept in memory only: a restart simply gives every task
// a fresh grace period.
type seedWatch struct {
	uploaded int64
	since    time.Time
}

// seedAutoRemoveReason reports why a finished BT task should be dropped from
// the task list, or "" when it should stay.
//
//	ratio  : uploaded/downloaded reached cfg.Ratio
//	idle   : no new uploaded bytes for cfg.IdleMinutes minutes
//
// A zero Ratio/IdleMinutes disables that condition on its own; a disabled or
// missing config disables both.
func seedAutoRemoveReason(cfg *base.SeedAutoRemoveConfig, downloaded, uploaded int64, idle time.Duration) string {
	if cfg == nil || !cfg.Enable {
		return ""
	}
	if cfg.Ratio > 0 && downloaded > 0 {
		if float64(uploaded)/float64(downloaded) >= cfg.Ratio {
			return "ratio"
		}
	}
	if cfg.IdleMinutes > 0 && idle >= time.Duration(cfg.IdleMinutes)*time.Minute {
		return "idle"
	}
	return ""
}

// startSeedAutoRemove launches the background loop. It never runs a check
// immediately, so a task that just finished gets a full interval of seeding
// before it can be dropped.
func (d *Downloader) startSeedAutoRemove() {
	go func() {
		watch := make(map[string]*seedWatch)
		for !d.closed.Load() {
			time.Sleep(seedAutoRemoveInterval)
			if d.closed.Load() {
				return
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						d.Logger.Error().Msgf("seed auto remove panic: %v", r)
					}
				}()
				d.seedAutoRemoveTick(watch)
			}()
		}
	}()
}

// seedAutoRemoveTick performs one inspection round. Tasks that are still
// extracting are skipped: deleting the record mid-extraction would leave the
// extract queue pointing at a task that no longer exists.
func (d *Downloader) seedAutoRemoveTick(watch map[string]*seedWatch) {
	cfg, err := d.GetConfig()
	if err != nil || cfg == nil || cfg.SeedAutoRemove == nil || !cfg.SeedAutoRemove.Enable {
		return
	}

	now := time.Now()
	alive := make(map[string]bool, len(watch))

	// Snapshot the task list under the lock: Delete() splices d.tasks in place,
	// and ranging over the live slice would skip the task that slides into the
	// freed slot.
	d.lock.Lock()
	tasks := append([]*Task(nil), d.tasks...)
	d.lock.Unlock()

	for _, task := range tasks {
		if task == nil || task.Progress == nil {
			continue
		}
		if task.Protocol != "bt" || task.Status != base.DownloadStatusDone {
			continue
		}
		switch task.Progress.ExtractStatus {
		case ExtractStatusQueued, ExtractStatusWaitingParts, ExtractStatusExtracting:
			continue
		}
		alive[task.ID] = true

		w := watch[task.ID]
		if w == nil {
			watch[task.ID] = &seedWatch{uploaded: task.Progress.Uploaded, since: now}
			continue
		}
		if task.Progress.Uploaded > w.uploaded {
			w.uploaded = task.Progress.Uploaded
			w.since = now
		}

		reason := seedAutoRemoveReason(cfg.SeedAutoRemove, task.Progress.Downloaded, task.Progress.Uploaded, now.Sub(w.since))
		if reason == "" {
			continue
		}
		// force=false: keep the downloaded files, only drop the task record.
		if err := d.Delete(&TaskFilter{IDs: []string{task.ID}}, false); err != nil {
			d.Logger.Error().Err(err).Msgf("seed auto remove: delete task %s failed", task.ID)
			continue
		}
		delete(watch, task.ID)
		d.Logger.Info().Msgf("seed auto remove: task %s removed (%s, ratio %.2f, uploaded %d)", task.ID, reason, ratioOf(task), task.Progress.Uploaded)
	}

	// Forget tasks that disappeared for other reasons (user deleted them).
	for id := range watch {
		if !alive[id] {
			delete(watch, id)
		}
	}
}

func ratioOf(task *Task) float64 {
	if task.Progress == nil || task.Progress.Downloaded == 0 {
		return 0
	}
	return float64(task.Progress.Uploaded) / float64(task.Progress.Downloaded)
}
