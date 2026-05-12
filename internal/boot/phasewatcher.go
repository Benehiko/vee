// Package boot tracks the phase of a starting VM by tailing its captured
// serial console log and matching well-known firmware/kernel/userspace
// markers. It is consumed by the start UX (the spinner) and persisted into
// VMState so vee status can show the current phase truthfully.
package boot

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"time"
)

// Phase is a coarse-grained label for where a VM is in its boot sequence.
// Phases monotonically advance (BIOSPost → Bootloader → KernelBoot → Init →
// CloudInit → Install → Ready); a Failed transition is terminal and carries a
// panic line. Install is a sub-phase of CloudInit used by templates that run a
// long install.sh script; it emits Detail strings naming the current stage.
type Phase string

const (
	PhaseUnknown    Phase = ""
	PhaseBIOSPost   Phase = "BIOS POST"
	PhaseBootloader Phase = "Bootloader"
	PhaseKernelBoot Phase = "Kernel boot"
	PhaseInit       Phase = "Init"
	PhaseCloudInit  Phase = "Cloud-init"
	PhaseInstall    Phase = "Installing"
	PhaseReady      Phase = "Ready"
	PhaseFailed     Phase = "Failed"
)

// rank returns the monotonic order of a phase. Failed and Ready are terminals
// and never transition back; intermediate phases must only advance forward.
func rank(p Phase) int {
	switch p {
	case PhaseBIOSPost:
		return 1
	case PhaseBootloader:
		return 2
	case PhaseKernelBoot:
		return 3
	case PhaseInit:
		return 4
	case PhaseCloudInit:
		return 5
	case PhaseInstall:
		return 6
	case PhaseReady:
		return 7
	default:
		return 0
	}
}

// Event describes a phase transition observed by a Watcher.
type Event struct {
	Phase     Phase
	Detail    string // optional, e.g. cloud-init module ratio
	PanicLine string // populated only on PhaseFailed (panic/oops/timeout)
	At        time.Time
}

type pattern struct {
	re    *regexp.Regexp
	phase Phase
	// fail signals this match terminates the boot as PhaseFailed; the matched
	// line is captured into Event.PanicLine.
	fail bool
	// detailGroup, when > 0, captures the nth submatch of re as Event.Detail.
	detailGroup int
}

// installStageLabels maps the stage= token from vee-install banners to a
// human-readable label shown in the spinner.
var installStageLabels = map[string]string{
	"clock-sync":       "waiting for clock sync",
	"clock-sync-done":  "clock synced",
	"network-wait":     "waiting for network",
	"network-ready":    "network ready",
	"partition":        "partitioning disk",
	"reflector":        "selecting mirrors",
	"reflector-done":   "mirrors selected",
	"reflector-failed": "mirrors: using fallback",
	"pacstrap":         "installing base system (this takes a while)",
	"pacstrap-done":    "base system installed",
	"fstab":            "generating fstab",
	"locale":           "configuring locale",
	"users":            "creating users",
	"services":         "enabling services",
	"grub":             "installing bootloader",
	"cleanup":          "finalising install",
	"done":             "install complete",
}

// patterns is evaluated in order on every fresh log line; first match wins.
// Failure patterns are checked first so a kernel panic during cloud-init still
// surfaces as Failed rather than being shadowed by a CloudInit match.
var patterns = []pattern{
	{regexp.MustCompile(`Kernel panic`), PhaseFailed, true, 0},
	{regexp.MustCompile(`end Kernel panic`), PhaseFailed, true, 0},
	{regexp.MustCompile(`Oops:`), PhaseFailed, true, 0},
	{regexp.MustCompile(`BUG:`), PhaseFailed, true, 0},
	{regexp.MustCompile(`No bootable device`), PhaseFailed, true, 0},
	{regexp.MustCompile(`==> vee-install: FAILED`), PhaseFailed, true, 0},

	// Fine-grained install stages emitted by install.sh via the serial port.
	// Checked before the generic CloudInit pattern so they promote to PhaseInstall.
	{regexp.MustCompile(`==> vee-install: stage=(\S+)`), PhaseInstall, false, 1},

	{regexp.MustCompile(`cloud-init.*finished`), PhaseReady, false, 0},
	{regexp.MustCompile(`cloud-init\[\d+\]:`), PhaseCloudInit, false, 0},
	{regexp.MustCompile(`Cloud-init v\.`), PhaseCloudInit, false, 0},

	{regexp.MustCompile(`Reached target`), PhaseInit, false, 0},
	{regexp.MustCompile(`systemd\[1\]:`), PhaseInit, false, 0},
	{regexp.MustCompile(`Welcome to `), PhaseInit, false, 0},

	{regexp.MustCompile(`Linux version `), PhaseKernelBoot, false, 0},
	{regexp.MustCompile(`^\[\s*0\.\d+\] `), PhaseKernelBoot, false, 0},
	{regexp.MustCompile(`Command line:`), PhaseKernelBoot, false, 0},

	{regexp.MustCompile(`GRUB version`), PhaseBootloader, false, 0},
	{regexp.MustCompile(`^Booting `), PhaseBootloader, false, 0},
	{regexp.MustCompile(`Loading initial ramdisk`), PhaseBootloader, false, 0},
	{regexp.MustCompile(`systemd-boot`), PhaseBootloader, false, 0},

	{regexp.MustCompile(`BdsDxe:`), PhaseBIOSPost, false, 0},
	{regexp.MustCompile(`UEFI Interactive Shell`), PhaseBIOSPost, false, 0},
	{regexp.MustCompile(`SeaBIOS`), PhaseBIOSPost, false, 0},
}

// Watcher tails a serial console log and emits Phase transitions on a channel.
type Watcher struct {
	path string
	// IdleTimeout, if non-zero, emits a synthetic Failed event when no log
	// activity is observed for the duration after the latest line.
	IdleTimeout time.Duration
	// PollInterval is how often the watcher re-reads the file when no new
	// data is available. Defaults to 200ms when zero.
	PollInterval time.Duration
}

// NewWatcher returns a Watcher that will tail the file at path. The file does
// not need to exist yet; the watcher polls until it appears.
func NewWatcher(path string) *Watcher {
	return &Watcher{path: path, IdleTimeout: 120 * time.Second, PollInterval: 200 * time.Millisecond}
}

// Run tails the serial log and writes Phase events to out. It returns when
// ctx is cancelled, when a terminal phase (Ready or Failed) has been emitted,
// or when IdleTimeout elapses without any new line being read. The caller
// owns the channel and should drain or close it after Run returns.
func (w *Watcher) Run(ctx context.Context, out chan<- Event) error {
	poll := w.PollInterval
	if poll == 0 {
		poll = 200 * time.Millisecond
	}

	var f *os.File
	var reader *bufio.Reader
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()

	openIfReady := func() {
		if f != nil {
			return
		}
		fh, err := os.Open(w.path)
		if err != nil {
			return
		}
		f = fh
		reader = bufio.NewReader(f)
	}

	last := Phase("")
	lastLineAt := time.Now()
	leftover := ""

	emit := func(ev Event) {
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		openIfReady()

		if reader != nil {
			progressed := false
			for {
				line, err := reader.ReadString('\n')
				if line != "" {
					if err == io.EOF {
						// Partial line — keep buffered for next iteration.
						leftover += line
						break
					}
					full := leftover + line
					leftover = ""
					lastLineAt = time.Now()
					progressed = true

					phase, fail, panicLine, detail := classify(full)
					if phase == PhaseUnknown {
						continue
					}
					if fail {
						emit(Event{Phase: PhaseFailed, PanicLine: panicLine, At: lastLineAt})
						return nil
					}
					// Install sub-phases re-emit at the same rank to surface detail
					// updates; all other phases must strictly advance forward.
					if phase == PhaseInstall {
						last = phase
						emit(Event{Phase: phase, Detail: detail, At: lastLineAt})
						continue
					}
					// Only advance forward; never regress.
					if rank(phase) <= rank(last) {
						continue
					}
					last = phase
					emit(Event{Phase: phase, Detail: detail, At: lastLineAt})
					if phase == PhaseReady {
						return nil
					}
				}
				if err != nil {
					if errors.Is(err, io.EOF) {
						break
					}
					return err
				}
			}
			_ = progressed
		}

		if w.IdleTimeout > 0 && time.Since(lastLineAt) > w.IdleTimeout && last != PhaseReady && last != PhaseFailed {
			emit(Event{Phase: PhaseFailed, PanicLine: "boot timeout: no serial activity for " + w.IdleTimeout.String(), At: time.Now()})
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// classify returns the phase for a line, whether the line is a terminal
// failure, the original line (for failure capture), and an optional detail
// string extracted from a capture group when detailGroup > 0.
func classify(line string) (phase Phase, fail bool, panicLine string, detail string) {
	for _, p := range patterns {
		m := p.re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		det := ""
		if p.detailGroup > 0 && p.detailGroup < len(m) {
			token := m[p.detailGroup]
			if label, ok := installStageLabels[token]; ok {
				det = label
			} else {
				det = token
			}
		}
		return p.phase, p.fail, line, det
	}
	return PhaseUnknown, false, "", ""
}
