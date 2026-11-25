package attendance

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// StatusAPI is the Mattermost custom-status API the updater needs.
type StatusAPI interface {
	GetCustomStatus(ctx context.Context, userID string) (*mattermost.CustomStatus, error)
	SetCustomStatus(ctx context.Context, userID string, cs mattermost.CustomStatus) error
	ClearCustomStatus(ctx context.Context, userID string) error
}

// StatusStore persists the member's own status while the bot overrides it.
type StatusStore interface {
	SaveCustomStatus(ctx context.Context, userID string, raw []byte) error
	TakeSavedCustomStatus(ctx context.Context, userID string) ([]byte, error)
}

// StatusKind is what the bot should show for a member.
type StatusKind int

const (
	StatusWorking StatusKind = iota
	StatusWorkingFromHome
	StatusOnBreak
	StatusOff // checked out: restore the member's own status
)

// Statuses the bot sets. Anything else is the member's own and is saved.
var botStatuses = map[StatusKind]mattermost.CustomStatus{
	StatusWorking:         {Emoji: "large_green_circle", Text: "Working"},
	StatusWorkingFromHome: {Emoji: "house", Text: "Working from home"},
	StatusOnBreak:         {Emoji: "coffee", Text: "On break"},
}

// IsBotStatus reports whether cs is one the bot sets.
func IsBotStatus(cs *mattermost.CustomStatus) bool {
	if cs == nil {
		return false
	}
	for _, b := range botStatuses {
		if cs.Emoji == b.Emoji && cs.Text == b.Text {
			return true
		}
	}
	return false
}

type statusJob struct {
	userID  string
	kind    StatusKind
	saveOwn bool // on check-in: save the member's own status first
}

// StatusUpdater applies custom statuses in the background, one at a time
// and in order, so a quick break→back never ends on the wrong status.
// Failures are logged and never affect check-in/out.
type StatusUpdater struct {
	api   StatusAPI
	store StatusStore
	log   *slog.Logger
	now   func() time.Time
	jobs  chan statusJob
}

// NewStatusUpdater creates the updater; call Run in a goroutine.
func NewStatusUpdater(api StatusAPI, store StatusStore, log *slog.Logger) *StatusUpdater {
	return &StatusUpdater{api: api, store: store, log: log, now: time.Now, jobs: make(chan statusJob, 256)}
}

// Enqueue schedules a status change without blocking. Nil-safe.
func (u *StatusUpdater) Enqueue(userID string, kind StatusKind, saveOwn bool) {
	if u == nil {
		return
	}
	select {
	case u.jobs <- statusJob{userID, kind, saveOwn}:
	default:
		u.log.Warn("custom status queue full; dropping update", "user_id", userID)
	}
}

// Run processes jobs until ctx is cancelled.
func (u *StatusUpdater) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-u.jobs:
			jctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			if err := u.apply(jctx, j); err != nil {
				u.log.Warn("custom status update failed", "user_id", j.userID, "kind", int(j.kind), "err", err)
			}
			cancel()
		}
	}
}

func (u *StatusUpdater) apply(ctx context.Context, j statusJob) error {
	if j.kind == StatusOff {
		return u.restore(ctx, j.userID)
	}
	if j.saveOwn {
		if err := u.saveOwn(ctx, j.userID); err != nil {
			// Not fatal: still show the working status.
			u.log.Warn("could not save member's own status", "user_id", j.userID, "err", err)
		}
	}
	return u.api.SetCustomStatus(ctx, j.userID, botStatuses[j.kind])
}

// saveOwn stores the member's current status unless it's empty or ours.
func (u *StatusUpdater) saveOwn(ctx context.Context, userID string) error {
	cur, err := u.api.GetCustomStatus(ctx, userID)
	if err != nil || cur.IsEmpty() || IsBotStatus(cur) {
		return err
	}
	raw, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	return u.store.SaveCustomStatus(ctx, userID, raw)
}

// restore puts back the saved status if still valid, otherwise clears —
// but only if the current status is ours (never wipe one the member set
// while checked in).
func (u *StatusUpdater) restore(ctx context.Context, userID string) error {
	raw, err := u.store.TakeSavedCustomStatus(ctx, userID)
	if err != nil {
		return err
	}
	cur, err := u.api.GetCustomStatus(ctx, userID)
	if err != nil {
		return err
	}
	if !cur.IsEmpty() && !IsBotStatus(cur) {
		return nil // member changed it themselves; leave it
	}
	if len(raw) > 0 {
		var saved mattermost.CustomStatus
		if err := json.Unmarshal(raw, &saved); err == nil && !saved.IsEmpty() && !saved.Expired(u.now()) {
			return u.api.SetCustomStatus(ctx, userID, saved)
		}
	}
	if cur.IsEmpty() {
		return nil
	}
	return u.api.ClearCustomStatus(ctx, userID)
}
