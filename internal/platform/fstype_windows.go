//go:build windows

package platform

// NetworkFilesystemName is unimplemented on Windows: vee's virtiofs shares
// require a Linux host, so --pg-data-dir is already rejected there.
func NetworkFilesystemName(dir string) string { return "" }
