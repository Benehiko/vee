package build

import (
	"testing"

	"github.com/Benehiko/vee/internal/vm"
)

func TestValidateHelios(t *testing.T) {
	good := func() *vm.VMConfig {
		return &vm.VMConfig{
			Template:   "windows",
			QemuBinary: "/opt/qemu-helios/bin/qemu-system-x86_64",
			GPU:        vm.GPUConfig{Mode: vm.GPUHelios},
		}
	}
	if err := validateGPUAccel(good(), Opts{HostMem: "16G"}); err != nil {
		t.Fatalf("valid helios config rejected: %v", err)
	}

	venus := true
	cases := map[string]struct {
		mut  func(*vm.VMConfig)
		opts Opts
	}{
		"non-windows template": {mut: func(c *vm.VMConfig) { c.Template = "desktop" }},
		"headless":             {mut: func(c *vm.VMConfig) { c.Headless = true }},
		"spice":                {mut: func(c *vm.VMConfig) { c.SPICE = &vm.SPICEConfig{Port: 5930} }},
		"no qemu binary":       {mut: func(c *vm.VMConfig) { c.QemuBinary = "" }},
		"venus flag":           {opts: Opts{Venus: &venus}},
		"helios flag without helios mode": {
			mut:  func(c *vm.VMConfig) { c.GPU.Mode = vm.GPUNone },
			opts: Opts{HeliosSetup: "HeliosSetup.exe"},
		},
	}
	for name, c := range cases {
		cfg := good()
		if c.mut != nil {
			c.mut(cfg)
		}
		if err := validateGPUAccel(cfg, c.opts); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
