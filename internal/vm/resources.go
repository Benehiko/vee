package vm

import "fmt"

// SetMemory updates the VM's configured memory size (e.g. "10G"). Takes
// effect the next time the VM starts.
func (m *Manager) SetMemory(name, memory string) error {
	if memory == "" {
		return fmt.Errorf("memory must not be empty")
	}
	cfg, err := m.loadConfig(name)
	if err != nil {
		return err
	}
	cfg.Memory = memory
	return m.saveConfig(cfg)
}

// SetCPUs updates the VM's configured vCPU count. Takes effect the next time
// the VM starts.
func (m *Manager) SetCPUs(name string, cpus int) error {
	if cpus <= 0 {
		return fmt.Errorf("cpus must be positive, got %d", cpus)
	}
	cfg, err := m.loadConfig(name)
	if err != nil {
		return err
	}
	cfg.CPUs = cpus
	return m.saveConfig(cfg)
}

// SetNICMode updates the VM's NIC mode ("user" or "bridge") and, for bridge
// mode, the bridge interface. Takes effect the next time the VM starts.
func (m *Manager) SetNICMode(name, mode, bridge string) error {
	if mode != "user" && mode != "bridge" {
		return fmt.Errorf("nic mode must be %q or %q, got %q", "user", "bridge", mode)
	}
	cfg, err := m.loadConfig(name)
	if err != nil {
		return err
	}
	cfg.NIC.Mode = mode
	if mode == "bridge" {
		if bridge == "" {
			bridge = "br0"
		}
		cfg.NIC.Bridge = bridge
	}
	return m.saveConfig(cfg)
}
