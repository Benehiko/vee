package vm

import "time"

type DiskConfig struct {
	Path      string `yaml:"path"`
	Size      string `yaml:"size"`
	Format    string `yaml:"format"`
	Interface string `yaml:"interface"`
	Media     string `yaml:"media"`
	Cache     string `yaml:"cache"`
	Readonly  bool   `yaml:"readonly"`
}

type NICConfig struct {
	Mode   string `yaml:"mode"`
	Bridge string `yaml:"bridge,omitempty"`
	Model  string `yaml:"model"`
	MAC    string `yaml:"mac"`
}

type GPUMode string

const (
	GPUNone        GPUMode = "none"
	GPUVirtio      GPUMode = "virtio"
	GPUPassthrough GPUMode = "passthrough"
)

type GPUConfig struct {
	Mode       GPUMode `yaml:"mode"`
	PCIAddr    string  `yaml:"pci_addr,omitempty"`
	AntiDetect bool    `yaml:"anti_detect,omitempty"`
}

type UEFIConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CodePath string `yaml:"code_path,omitempty"`
	VarsPath string `yaml:"vars_path"`
}

type SPICEConfig struct {
	Port             int  `yaml:"port"`
	DisableTicketing bool `yaml:"disable_ticketing"`
}

type VirtiofsMount struct {
	SocketPath string `yaml:"socket_path"`
	SharedDir  string `yaml:"shared_dir"`
	Tag        string `yaml:"tag"`
}

type TPMConfig struct {
	Enabled bool `yaml:"enabled"`
}

// CloudInitConfig carries first-boot configuration rendered into a cidata ISO.
type CloudInitConfig struct {
	Hostname string   `yaml:"hostname,omitempty"`
	User     string   `yaml:"user,omitempty"`
	SSHKeys  []string `yaml:"ssh_keys,omitempty"`
	Packages []string `yaml:"packages,omitempty"`
	RunCmds  []string `yaml:"run_cmds,omitempty"`
}

type VMConfig struct {
	Name           string           `yaml:"name"`
	Template       string           `yaml:"template"`
	Memory         string           `yaml:"memory"`
	CPUs           int              `yaml:"cpus"`
	Sockets        int              `yaml:"sockets"`
	Cores          int              `yaml:"cores"`
	Threads        int              `yaml:"threads"`
	CPUModel       string           `yaml:"cpu_model"`
	CPUFlags       []string         `yaml:"cpu_flags,omitempty"`
	Disks          []DiskConfig     `yaml:"disks"`
	NIC            NICConfig        `yaml:"nic"`
	GPU            GPUConfig        `yaml:"gpu,omitempty"`
	UEFI           UEFIConfig       `yaml:"uefi,omitempty"`
	SPICE          *SPICEConfig     `yaml:"spice,omitempty"`
	VirtiofsMounts []VirtiofsMount  `yaml:"virtiofs_mounts,omitempty"`
	CloudInit      *CloudInitConfig `yaml:"cloud_init,omitempty"`
	TPM            *TPMConfig       `yaml:"tpm,omitempty"`
	SSHShare       bool             `yaml:"ssh_share,omitempty"`
	VsockCID       uint32           `yaml:"vsock_cid,omitempty"`
	CreatedAt      time.Time        `yaml:"created_at"`
}
