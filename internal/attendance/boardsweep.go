package attendance

import (
	"context"
	"strings"
	"time"
)

// The board sweep keeps each team's GitHub Project complete: open issues in
// the team's org assigned to a linked member are added to it, from any
// repo, within SweepEvery even when nobody runs a command. A run looks only
// at issues updated since the previous run (creating or assigning an issue
// updates it), with sweepOverlap to cover GitHub's search index lag; a full
// run (all open issues) happens at startup and daily at 03:00.

const (
	sweepOverlap = time.Minute
	sweepTimeout = 90 * time.Second
)

// SweepBoards runs the sweep for every team with a Project. A run that's
// still going makes the next one skip; a failed team keeps its old
// watermark, so the next run covers the gap.
func (j *Jobs) SweepBoards(ctx context.Context, full bool) {
	if j.Board == nil {
		return
	}
	if !j.sweepMu.TryLock() {
		j.Log.Info("board sweep still running; skipped")
		return
	}
	defer j.sweepMu.Unlock()
	if j.lastSweep == nil {
		j.lastSweep = map[string]time.Time{}
	}

	settings, err := j.Store.AllMembers(ctx)
	if err != nil {
		j.Log.Error("board sweep: members", "err", err)
		return
	}
	linked := map[string]bool{}
	for _, ms := range settings {
		if ms.GitHubLogin != "" {
			linked[strings.ToLower(ms.GitHubLogin)] = true
		}
	}
	if len(linked) == 0 {
		return
	}
	start := j.Now()
	for _, t := range j.Teams {
		if !j.Board.Configured(t.ID) {
			continue
		}
		var since time.Time // zero: all open issues
		if last, ok := j.lastSweep[t.ID]; ok && !full {
			since = last.Add(-sweepOverlap)
		}
		sctx, cancel := context.WithTimeout(ctx, sweepTimeout)
		n, err := j.Board.SweepBoard(sctx, t.ID, since, linked)
		cancel()
		if err != nil {
			j.Log.Warn("board sweep failed; the next run retries", "team", t.Name, "full", since.IsZero(), "err", err)
			continue
		}
		j.lastSweep[t.ID] = start
		if n > 0 || since.IsZero() {
			j.Log.Info("board sweep done", "team", t.Name, "full", since.IsZero(), "added", n)
		}
	}
}
