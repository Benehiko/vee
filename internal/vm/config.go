package vm

import "time"

type DiskConfig struct {
	Path        string `yaml:"path"         json:"path"`
	Size        string `yaml:"size"         json:"size"`
	Format      string `yaml:"format"       json:"format"`
	Interface   string `yaml:"interface"    json:"interface"`
	Media       string `yaml:"media"        json:"media"`
	Cache       string `yaml:"cache"        json:"cache"`
	Readonly    bool   `yaml:"readonly"     json:"readonly"`
	BackingFile string `yaml:"backing_file,omitempty" json:"backing_file,omitempty"`
	// Serial is passed to the virtio-blk-pci device (passthrough disks).
	Serial string `yaml:"serial,omitempty" json:"serial,omitempty"`
	// Passthrough marks this as a raw host block device (e.g. /dev/disk/by-id/...).
	// Path must point to the host device. Format, cache, aio are set automatically.
	Passthrough bool `yaml:"passthrough,omitempty" json:"passthrough,omitempty"`
	// InstallISO marks a cdrom disk as a one-shot installer image. vee attaches
	// it on the first boot (InstallState = pending) and removes it from the
	// saved config once the install is complete (InstallState = ready), so the
	// VM never re-enters the installer on subsequent starts.
	InstallISO bool `yaml:"install_iso,omitempty" json:"install_iso,omitempty"`
}

type NICConfig struct {
	Mode     string   `yaml:"mode"              json:"mode"`
	Bridge   string   `yaml:"bridge,omitempty"  json:"bridge,omitempty"`
	Model    string   `yaml:"model"             json:"model"`
	MAC      string   `yaml:"mac"               json:"mac"`
	HostFwds []string `yaml:"host_fwds,omitempty" json:"host_fwds,omitempty"`
}

type GPUMode string

const (
	GPUNone        GPUMode = "none"
	GPUVirtio      GPUMode = "virtio"
	GPUPassthrough GPUMode = "passthrough"
)

type GPUConfig struct {
	Mode    GPUMode `yaml:"mode"              json:"mode"`
	PCIAddr string  `yaml:"pci_addr,omitempty" json:"pci_addr,omitempty"`
	// ExtraVFIOAddrs lists additional PCI addresses that must be passed through
	// alongside the primary GPU — typically the GPU's HDMI/DP audio function
	// (e.g. "0000:08:00.1"). All addresses in the same IOMMU reset domain must
	// be owned by the same VFIO container.
	ExtraVFIOAddrs []string `yaml:"extra_vfio_addrs,omitempty" json:"extra_vfio_addrs,omitempty"`
	// ROMFile is a path to a VBIOS dump to pass to the guest via romfile=.
	// Required on some AMD GPUs when rombar=1 alone is insufficient for the
	// guest driver to initialize display output.
	ROMFile    string `yaml:"rom_file,omitempty"   json:"rom_file,omitempty"`
	AntiDetect bool   `yaml:"anti_detect,omitempty" json:"anti_detect,omitempty"`
}

type UEFIConfig struct {
	Enabled  bool   `yaml:"enabled"            json:"enabled"`
	CodePath string `yaml:"code_path,omitempty" json:"code_path,omitempty"`
	VarsPath string `yaml:"vars_path"          json:"vars_path"`
}

type SPICEConfig struct {
	Port             int  `yaml:"port"               json:"port"`
	DisableTicketing bool `yaml:"disable_ticketing"  json:"disable_ticketing"`
}

type VirtiofsMount struct {
	SocketPath string `yaml:"socket_path" json:"socket_path"`
	SharedDir  string `yaml:"shared_dir"  json:"shared_dir"`
	Tag        string `yaml:"tag"         json:"tag"`
}

type TPMConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// CloudInitWriteFile describes a file to drop on first boot.
type CloudInitWriteFile struct {
	Path        string `yaml:"path"                  json:"path"`
	Content     string `yaml:"content"               json:"content"`
	Permissions string `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Owner       string `yaml:"owner,omitempty"       json:"owner,omitempty"`
	Defer       bool   `yaml:"defer,omitempty"       json:"defer,omitempty"`
}

// CloudInitConfig carries first-boot configuration rendered into a cidata ISO.
type CloudInitConfig struct {
	Hostname    string               `yaml:"hostname,omitempty"     json:"hostname,omitempty"`
	User        string               `yaml:"user,omitempty"         json:"user,omitempty"`
	DefaultUser string               `yaml:"default_user,omitempty" json:"default_user,omitempty"`
	SSHKeys     []string             `yaml:"ssh_keys,omitempty"     json:"ssh_keys,omitempty"`
	Packages    []string             `yaml:"packages,omitempty"     json:"packages,omitempty"`
	RunCmds     []string             `yaml:"run_cmds,omitempty"     json:"run_cmds,omitempty"`
	WriteFiles  []CloudInitWriteFile `yaml:"write_files,omitempty"  json:"write_files,omitempty"`
}

// ServiceProtocol describes how a service should be accessed.
type ServiceProtocol string

const (
	ServiceHTTP  ServiceProtocol = "http"
	ServiceHTTPS ServiceProtocol = "https"
	ServiceSPICE ServiceProtocol = "spice"
	ServiceTCP   ServiceProtocol = "tcp"
)

// ServiceEntry declares a named guest service that vee tunnel can connect to.
// Port is the port inside the VM (or on the host for user-mode HostFwds).
type ServiceEntry struct {
	Name     string          `yaml:"name"     json:"name"`
	Port     int             `yaml:"port"     json:"port"`
	Protocol ServiceProtocol `yaml:"protocol" json:"protocol"`
}

type VMConfig struct {
	Name           string           `yaml:"name"                    json:"name"`
	Template       string           `yaml:"template"                json:"template"`
	Memory         string           `yaml:"memory"                  json:"memory"`
	CPUs           int              `yaml:"cpus"                    json:"cpus"`
	Sockets        int              `yaml:"sockets"                 json:"sockets"`
	Cores          int              `yaml:"cores"                   json:"cores"`
	Threads        int              `yaml:"threads"                 json:"threads"`
	CPUModel       string           `yaml:"cpu_model"               json:"cpu_model"`
	CPUFlags       []string         `yaml:"cpu_flags,omitempty"     json:"cpu_flags,omitempty"`
	Disks          []DiskConfig     `yaml:"disks"                   json:"disks"`
	NIC            NICConfig        `yaml:"nic"                     json:"nic"`
	GPU            GPUConfig        `yaml:"gpu,omitempty"           json:"gpu,omitempty"`
	UEFI           UEFIConfig       `yaml:"uefi,omitempty"          json:"uefi,omitempty"`
	SPICE          *SPICEConfig     `yaml:"spice,omitempty"         json:"spice,omitempty"`
	VirtiofsMounts []VirtiofsMount  `yaml:"virtiofs_mounts,omitempty" json:"virtiofs_mounts,omitempty"`
	CloudInit      *CloudInitConfig `yaml:"cloud_init,omitempty"    json:"cloud_init,omitempty"`
	TPM            *TPMConfig       `yaml:"tpm,omitempty"           json:"tpm,omitempty"`
	SSHUser        string           `yaml:"ssh_user,omitempty"      json:"ssh_user,omitempty"`
	SSHShare       bool             `yaml:"ssh_share,omitempty"     json:"ssh_share,omitempty"`
	VsockCID       uint32           `yaml:"vsock_cid,omitempty"     json:"vsock_cid,omitempty"`
	Headless       bool             `yaml:"headless,omitempty"      json:"headless,omitempty"`
	SSHPort        int              `yaml:"ssh_port,omitempty"      json:"ssh_port,omitempty"`
	GuestAgent     bool             `yaml:"guest_agent,omitempty"   json:"guest_agent,omitempty"`
	ExtraDevices   []string         `yaml:"extra_devices,omitempty" json:"extra_devices,omitempty"`
	VGA            string           `yaml:"vga,omitempty"           json:"vga,omitempty"`
	Hostname       string           `yaml:"hostname,omitempty"      json:"hostname,omitempty"`
	TrueNASAPIKey  string           `yaml:"truenas_api_key,omitempty" json:"truenas_api_key,omitempty"`
	TrueNASUser    string           `yaml:"truenas_user,omitempty"  json:"truenas_user,omitempty"`
	VPNProvider    string           `yaml:"vpn_provider,omitempty"  json:"vpn_provider,omitempty"`
	// Services lists named guest services available via vee tunnel.
	Services []ServiceEntry `yaml:"services,omitempty" json:"services,omitempty"`
	// CPUPinning is a list of host CPU indices to pin the VM's vCPU threads to
	// (e.g. [4,5,6,7]). Empty means no pinning. The host kernel can still
	// schedule other work on those cores; add isolcpus= to the host kernel
	// cmdline for full isolation.
	CPUPinning []int `yaml:"cpu_pinning,omitempty" json:"cpu_pinning,omitempty"`
	// RTC overrides the -rtc argument (e.g. "base=localtime,clock=host").
	// Leave empty for the default (UTC). Set for Windows and gaming VMs.
	RTC       string    `yaml:"rtc,omitempty"         json:"rtc,omitempty"`
	CreatedAt time.Time `yaml:"created_at"            json:"created_at"`
}
