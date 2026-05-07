package monitor

import (
	"context"
	"time"

	"github.com/Benehiko/vee/qemu"
)

// Poller reads VM stats from QMP on a fixed interval and sends them to Ch.
type Poller struct {
	Ch     <-chan Stats
	client *qemu.QMPClient
	cancel context.CancelFunc
}

// NewPoller dials the QMP socket and starts polling every interval.
func NewPoller(ctx context.Context, socketPath string, interval time.Duration) (*Poller, error) {
	client, err := qemu.NewQMPClient(socketPath, 5*time.Second)
	if err != nil {
		return nil, err
	}

	ch := make(chan Stats, 4)
	ctx, cancel := context.WithCancel(ctx)
	p := &Poller{Ch: ch, client: client, cancel: cancel}
	go p.loop(ctx, ch, interval)
	return p, nil
}

// Close stops polling and disconnects from QMP.
func (p *Poller) Close() {
	p.cancel()
	_ = p.client.Close()
}

func (p *Poller) loop(ctx context.Context, ch chan<- Stats, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var prev qemu.QMPRawCounters
	first := true

	for {
		select {
		case <-ctx.Done():
			close(ch)
			return
		case <-ticker.C:
			cur, err := p.client.QueryRaw()
			if err != nil {
				continue
			}
			if first {
				prev = cur
				first = false
				continue
			}
			ch <- countersToStats(prev, cur)
			prev = cur
		}
	}
}

func countersToStats(prev, cur qemu.QMPRawCounters) Stats {
	return Stats{
		MemActual:      cur.BalloonActual,
		DiskReadBytes:  delta(prev.DiskRdBytes, cur.DiskRdBytes),
		DiskWriteBytes: delta(prev.DiskWrBytes, cur.DiskWrBytes),
		NetRxBytes:     delta(prev.NetRxBytes, cur.NetRxBytes),
		NetTxBytes:     delta(prev.NetTxBytes, cur.NetTxBytes),
	}
}

func delta(prev, cur uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	return cur
}
