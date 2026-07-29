package vm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/Benehiko/vee/internal/vzfirstboot"
	"github.com/Benehiko/vee/provider"
)

// recordingCore keeps the log entries a test asserts on. The grant path
// deliberately reports "no privacy database yet" at Debug and a real failure at
// Warn — both leave the guest pending, so the level is the only thing that says
// which one happened.
type recordingCore struct {
	entries *[]zapcore.Entry
}

func (c recordingCore) Enabled(zapcore.Level) bool        { return true }
func (c recordingCore) With([]zapcore.Field) zapcore.Core { return c }
func (c recordingCore) Sync() error                       { return nil }
func (c recordingCore) Write(e zapcore.Entry, _ []zapcore.Field) error {
	*c.entries = append(*c.entries, e)
	return nil
}

func (c recordingCore) Check(e zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	return ce.AddCore(e, c)
}

// grantProvider is the slice of provider.Provider the grant path uses: a
// logger, and a storage path to persist the config into.
type grantProvider struct {
	cfg     *provider.Config
	entries *[]zapcore.Entry
}

func (p grantProvider) Config() *provider.Config { return p.cfg }
func (p grantProvider) Logger() *zap.Logger      { return zap.New(recordingCore{entries: p.entries}) }
func (p grantProvider) DB() *sql.DB              { return nil }

// grantMachine is a vzMachine whose config is persisted to a temp storage path,
// so a cleared flag can be read back the way a later start would read it.
type grantMachine struct {
	*vzMachine
	cfg     *VMConfig
	entries *[]zapcore.Entry
}

// levels reports the levels logged so far, in order.
func (g grantMachine) levels() []zapcore.Level {
	out := make([]zapcore.Level, 0, len(*g.entries))
	for _, e := range *g.entries {
		out = append(out, e.Level)
	}
	return out
}

func newGrantMachine(t *testing.T, pending bool) grantMachine {
	t.Helper()
	cfg := &VMConfig{
		Name:    "grant-test",
		Backend: "vz",
		MacOS:   &MacOSConfig{ScreenSharingGrantPending: pending},
	}
	entries := &[]zapcore.Entry{}
	m := &Manager{provider: grantProvider{
		cfg:     &provider.Config{StoragePath: t.TempDir()},
		entries: entries,
	}}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return grantMachine{
		vzMachine: &vzMachine{manager: m, name: cfg.Name, cfg: cfg, diskPath: "/nonexistent/disk.img"},
		cfg:       cfg,
		entries:   entries,
	}
}

// stubGrants replaces the grant call for the duration of a test.
func stubGrants(t *testing.T, granted bool, err error) *int {
	t.Helper()
	calls := 0
	orig := ensureScreenSharingGrants
	ensureScreenSharingGrants = func(context.Context, string) (bool, error) {
		calls++
		return granted, err
	}
	t.Cleanup(func() { ensureScreenSharingGrants = orig })
	return &calls
}

// pendingInStore reports the flag a later start would load.
func pendingInStore(t *testing.T, g grantMachine) bool {
	t.Helper()
	reloaded, err := g.manager.loadConfig(g.cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	return reloaded.MacOS.ScreenSharingGrantPending
}

func TestScreenSharingGrantSkippedWhenNotPending(t *testing.T) {
	// A guest that never asked for Screen Sharing — or one already granted —
	// must not pay for a disk attach at every start.
	for _, tc := range []struct {
		name string
		cfg  *MacOSConfig
	}{
		{"grant already applied", &MacOSConfig{}},
		{"not a provisioned guest", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubGrants(t, true, nil)
			g := newGrantMachine(t, false)
			g.cfg.MacOS = tc.cfg
			if err := g.grantScreenSharing(t.Context()); err != nil {
				t.Fatalf("grantScreenSharing: %v", err)
			}
			if *calls != 0 {
				t.Errorf("attached the guest disk %d times, want 0", *calls)
			}
		})
	}
}

func TestScreenSharingGrantClearsPendingOnce(t *testing.T) {
	calls := stubGrants(t, true, nil)
	g := newGrantMachine(t, true)

	if err := g.grantScreenSharing(t.Context()); err != nil {
		t.Fatalf("grantScreenSharing: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("grant calls = %d, want 1", *calls)
	}
	if g.cfg.MacOS.ScreenSharingGrantPending {
		t.Error("the pending flag survived a successful grant, so every later start would re-attach the disk")
	}
	// The next start must see the cleared flag, not just this in-memory config.
	if pendingInStore(t, g) {
		t.Error("the cleared flag was not persisted")
	}

	// A second start does no work.
	if err := g.grantScreenSharing(t.Context()); err != nil {
		t.Fatalf("second grantScreenSharing: %v", err)
	}
	if *calls != 1 {
		t.Errorf("grant calls after a second start = %d, want 1", *calls)
	}
}

func TestScreenSharingGrantAlreadyDoneStopsRetrying(t *testing.T) {
	// Nothing to write, because the rows are already there — a guest granted
	// out of band, or one whose config was hand-edited. Still worth recording,
	// so the disk stops being attached.
	stubGrants(t, false, nil)
	g := newGrantMachine(t, true)

	if err := g.grantScreenSharing(t.Context()); err != nil {
		t.Fatalf("grantScreenSharing: %v", err)
	}
	if g.cfg.MacOS.ScreenSharingGrantPending || pendingInStore(t, g) {
		t.Error("an already-granted guest stayed pending, so it would attach its disk at every start")
	}
}

func TestScreenSharingGrantRetriesBeforeFirstBoot(t *testing.T) {
	// A guest that has never booted has no privacy database yet. That is the
	// normal case for a freshly restored guest, and the whole reason the grant
	// moved to start: it must stay pending so the next start retries.
	calls := stubGrants(t, false, fmt.Errorf("read grant: %w", vzfirstboot.ErrNoTCCDB))
	g := newGrantMachine(t, true)

	if err := g.grantScreenSharing(t.Context()); err != nil {
		t.Fatalf("grantScreenSharing: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("grant calls = %d, want 1", *calls)
	}
	if !g.cfg.MacOS.ScreenSharingGrantPending || !pendingInStore(t, g) {
		t.Error("gave up on a guest that has not booted yet; Screen Sharing would never be authorized")
	}
	// Expected, not a problem: a guest restored minutes ago hits this on its
	// first start and the user has nothing to act on.
	if levels := g.levels(); len(levels) != 1 || levels[0] != zapcore.DebugLevel {
		t.Errorf("logged %v, want one debug entry — the pre-first-boot state is not a warning", levels)
	}
}

func TestScreenSharingGrantGivesUpOnAnUnknownDatabase(t *testing.T) {
	// An unrecognized privacy database never becomes recognizable, so retrying
	// would attach the guest disk at every start for the life of the VM.
	calls := stubGrants(t, false, fmt.Errorf("open: %w", vzfirstboot.ErrUnknownTCCSchema))
	g := newGrantMachine(t, true)

	if err := g.grantScreenSharing(t.Context()); err != nil {
		t.Fatalf("grantScreenSharing: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("grant calls = %d, want 1", *calls)
	}
	if g.cfg.MacOS.ScreenSharingGrantPending || pendingInStore(t, g) {
		t.Error("kept retrying a database vee will never understand")
	}
	if levels := g.levels(); len(levels) != 1 || levels[0] != zapcore.WarnLevel {
		t.Errorf("logged %v, want one warning — the user has to enable Screen Sharing themselves", levels)
	}
}

func TestScreenSharingGrantFailureDoesNotFailTheStart(t *testing.T) {
	// The guest boots fine without the grant, so a real failure is a warning
	// and stays pending for the next attempt.
	calls := stubGrants(t, false, fmt.Errorf("mount data volume: exit status 1"))
	g := newGrantMachine(t, true)

	if err := g.grantScreenSharing(t.Context()); err != nil {
		t.Fatalf("a failed grant must not fail the start: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("grant calls = %d, want 1", *calls)
	}
	if !g.cfg.MacOS.ScreenSharingGrantPending || !pendingInStore(t, g) {
		t.Error("a failed grant cleared the pending flag, so it would never be retried")
	}
	if levels := g.levels(); len(levels) != 1 || levels[0] != zapcore.WarnLevel {
		t.Errorf("logged %v, want one warning — this one is worth telling the user about", levels)
	}
}

func TestScreenSharingGrantAbortsStartWhenTheDiskIsStillAttached(t *testing.T) {
	// The one failure that must stop the start: Virtualization.framework cannot
	// open a disk the host still has attached, and a mount point left inside the
	// VM directory would make `vee delete` walk the guest's own file system.
	stubGrants(t, false, fmt.Errorf("unmount: %w", vzfirstboot.ErrGuestDiskBusy))
	g := newGrantMachine(t, true)

	err := g.grantScreenSharing(t.Context())
	if !errors.Is(err, vzfirstboot.ErrGuestDiskBusy) {
		t.Fatalf("grantScreenSharing = %v, want ErrGuestDiskBusy", err)
	}
	if !g.cfg.MacOS.ScreenSharingGrantPending || !pendingInStore(t, g) {
		t.Error("gave up on a grant that never ran")
	}
}

func TestStartDetachedAttemptsTheGrant(t *testing.T) {
	// Wiring test: the grant must be attempted by the start path itself, and
	// before the helper is spawned — the disk has to be free while it runs.
	calls := stubGrants(t, true, nil)
	g := newGrantMachine(t, true)
	g.vmDir = t.TempDir()
	g.helperPath = "/nonexistent/vee-vz-helper"

	if _, err := g.StartDetached(t.Context()); err == nil {
		t.Fatal("a helper path that does not exist must fail the start")
	}
	if *calls != 1 {
		t.Errorf("the start path attempted the grant %d times, want 1", *calls)
	}
}

func TestStartDetachedAbortsWhenTheDiskIsStillAttached(t *testing.T) {
	// The helper must not be spawned at all when the disk was left attached,
	// so the error has to reach the caller rather than be logged and dropped.
	stubGrants(t, false, fmt.Errorf("detach: %w", vzfirstboot.ErrGuestDiskBusy))
	g := newGrantMachine(t, true)
	g.vmDir = t.TempDir()
	// A helper that would succeed if it were ever reached.
	g.helperPath = "/usr/bin/true"

	_, err := g.StartDetached(t.Context())
	if !errors.Is(err, vzfirstboot.ErrGuestDiskBusy) {
		t.Fatalf("StartDetached = %v, want ErrGuestDiskBusy", err)
	}
}
