package vm

import (
	"fmt"
	"strconv"
	"strings"
)

// ResolvedService is a VM's declared service with runtime port resolution
// applied (SPICE's port lives in state after first start).
type ResolvedService struct {
	ServiceEntry
}

// ResolvedServices returns cfg's services with runtime ports resolved from
// state. Shared by `vee tunnel` and the MCP server's vm_services.
func ResolvedServices(cfg *VMConfig, state *VMState) []ResolvedService {
	var out []ResolvedService
	for _, s := range cfg.Services {
		rs := ResolvedService{s}
		// SPICE port lives in state after first start.
		if s.Protocol == ServiceSPICE {
			if state.SPICEPort > 0 {
				rs.Port = state.SPICEPort
			} else if cfg.SPICE != nil {
				rs.Port = cfg.SPICE.Port
			}
		}
		out = append(out, rs)
	}
	return out
}

// ServiceURL describes how to reach a resolved service from the host: a
// direct URL when the port is already host-bound (SPICE, user-mode hostfwd),
// or a placeholder indicating a proxy/tunnel is needed.
func ServiceURL(cfg *VMConfig, s ResolvedService) string {
	// SPICE is always bound on the host by QEMU — show direct URL.
	if s.Protocol == ServiceSPICE {
		return fmt.Sprintf("spice://localhost:%d", s.Port)
	}
	// user-mode with hostfwd — port is already on the host.
	if cfg.NIC.Mode == "user" {
		if hostPort := FindHostFwd(cfg.NIC.HostFwds, s.Port); hostPort > 0 {
			switch s.Protocol {
			case ServiceHTTP:
				return fmt.Sprintf("http://localhost:%d", hostPort)
			case ServiceHTTPS:
				return fmt.Sprintf("https://localhost:%d", hostPort)
			default:
				return fmt.Sprintf("localhost:%d", hostPort)
			}
		}
	}
	// Bridge / no hostfwd — a proxy will be opened on a random local port.
	switch s.Protocol {
	case ServiceHTTP:
		return fmt.Sprintf("http://localhost:<proxy> → guest:%d", s.Port)
	case ServiceHTTPS:
		return fmt.Sprintf("https://localhost:<proxy> → guest:%d", s.Port)
	default:
		return fmt.Sprintf("localhost:<proxy> → guest:%d", s.Port)
	}
}

// FindHostFwd returns the host port for a forwarded guest port, or 0 if none.
// HostFwds format: "tcp:127.0.0.1:<hostPort>-:<guestPort>".
func FindHostFwd(fwds []string, guestPort int) int {
	guestStr := strconv.Itoa(guestPort)
	for _, fwd := range fwds {
		// format: proto:hostAddr:hostPort-:guestPort
		parts := strings.SplitN(fwd, "-:", 2)
		if len(parts) != 2 || parts[1] != guestStr {
			continue
		}
		// hostPart = "tcp:127.0.0.1:hostPort"
		hostPart := strings.SplitN(parts[0], ":", 3)
		if len(hostPart) == 3 {
			if p, err := strconv.Atoi(hostPart[2]); err == nil {
				return p
			}
		}
	}
	return 0
}
