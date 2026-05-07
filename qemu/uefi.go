package qemu

type UEFI struct {
	CodePath string
	VarsPath string
}

var _ Builder = &UEFI{}

func NewUEFI(codePath, varsPath string) *UEFI {
	return &UEFI{CodePath: codePath, VarsPath: varsPath}
}

func (u *UEFI) Args() []string {
	return []string{
		"-drive", "if=pflash,format=raw,readonly=on,file=" + u.CodePath,
		"-drive", "if=pflash,format=raw,file=" + u.VarsPath,
	}
}
