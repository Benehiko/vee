package cloudinit

// MDNSPackages returns the package list required to publish the VM's hostname
// over multicast DNS (Avahi). Combine with MDNSRunCmds and MDNSFirewallCmds.
func MDNSPackages(distro Distro) []string {
	switch distro {
	case Ubuntu:
		return []string{"avahi-daemon", "libnss-mdns"}
	case Arch:
		return []string{"avahi", "nss-mdns"}
	case Fedora:
		return []string{"avahi", "nss-mdns"}
	default:
		return []string{"avahi-daemon"}
	}
}

// MDNSRunCmds returns the runcmd entries that enable the Avahi daemon.
func MDNSRunCmds() []string {
	return []string{
		"systemctl enable --now avahi-daemon",
	}
}

// MDNSFirewallCmds returns firewall rules that allow inbound mDNS (UDP 5353).
// The chosen firewall must already be installed; pass the firewall in use by
// the template (ufw on Ubuntu/Arch defaults, firewalld on Fedora).
func MDNSFirewallCmds(firewall string) []string {
	switch firewall {
	case "ufw":
		return []string{"ufw allow 5353/udp"}
	case "firewalld":
		return []string{
			"firewall-cmd --permanent --add-service=mdns",
			"firewall-cmd --reload",
		}
	default:
		return nil
	}
}
