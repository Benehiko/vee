package mirror

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Mode is the mirror resolution mode requested by the user / config.
type Mode string

const (
	ModeAuto Mode = "auto" // use if pacoloco is currently active
	ModeOn   Mode = "on"   // require pacoloco to be active; warn if not
	ModeOff  Mode = "off"  // never inject
)

// ParseMode normalises a user-supplied string into a Mode. Unknown values
// fall back to ModeAuto.
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "force":
		return ModeOn
	case "off", "disabled", "false":
		return ModeOff
	default:
		return ModeAuto
	}
}

// Decision describes whether mirror injection should happen and where the
// guest should point pacman.
type Decision struct {
	Enabled    bool   // inject mirror entry into the guest
	GuestURL   string // pacman Server line to inject (only valid when Enabled)
	HostActive bool   // whether the host pacoloco unit is currently active
	Reason     string // human-readable explanation for log/CLI output
}

// Resolve decides whether the mirror should be used for a VM with the given
// nicMode. configAddr is the host:port the guest should reach (defaults to
// 10.0.2.2:9129 when empty).
//
// Rules:
//   - ModeOff → never inject.
//   - Bridge networking → cannot reach 10.0.2.2; skip with reason.
//   - ModeOn → inject (even if unit is not currently active — guests retry).
//   - ModeAuto → inject only when the unit is active.
func Resolve(ctx context.Context, mode Mode, nicMode, configAddr string) Decision {
	if mode == ModeOff {
		return Decision{Reason: "mirror disabled via --mirror=off"}
	}
	if nicMode == "bridge" && (configAddr == "" || strings.HasPrefix(configAddr, GuestGatewayAddress+":")) {
		return Decision{Reason: "bridge networking: 10.0.2.2 gateway unreachable; set provider mirror_address to a routable host IP to enable"}
	}
	active := isUnitActive(ctx)
	if mode == ModeAuto && !active {
		return Decision{HostActive: false, Reason: "auto: pacoloco unit not active; run `vee mirror start`"}
	}
	return Decision{
		Enabled:    true,
		GuestURL:   GuestMirrorURL(configAddr),
		HostActive: active,
		Reason:     "",
	}
}

func isUnitActive(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "systemctl", "--user", "is-active", UnitName)
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out)) == "active"
}

// PacmanMirrorlistContent returns the contents of /etc/pacman.d/mirrorlist
// that wires the guest to the host cache while keeping an upstream fallback.
func PacmanMirrorlistContent(guestURL string) string {
	return fmt.Sprintf(`# Managed by vee — host pacoloco cache, falls back to upstream.
Server = %s
Server = https://geo.mirror.pkgbuild.com/$repo/os/$arch
`, guestURL)
}
