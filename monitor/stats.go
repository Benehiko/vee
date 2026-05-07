package monitor

// Stats is a snapshot of VM resource usage polled via QMP.
type Stats struct {
	// CPU utilisation across all vCPUs, 0.0–1.0
	CPUPercent float64
	// Guest balloon memory in bytes (reported by query-balloon)
	MemActual uint64
	// Disk I/O: bytes read and written since last sample
	DiskReadBytes  uint64
	DiskWriteBytes uint64
	// Network I/O: bytes received and sent since last sample
	NetRxBytes uint64
	NetTxBytes uint64
}
