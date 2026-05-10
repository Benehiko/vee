package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Benehiko/vee/internal/monitor"
	"github.com/Benehiko/vee/internal/qemu"
	"github.com/Benehiko/vee/internal/vm"
)

// VMStats holds the latest polled stats for one VM.
type VMStats struct {
	Name         string        `json:"name"`
	Template     string        `json:"template"`
	Running      bool          `json:"running"`
	InstallState string        `json:"install_state,omitempty"`
	PID          int           `json:"pid,omitempty"`
	Stats        monitor.Stats `json:"stats"`
}

// Dashboard serves a JSON API and a simple HTML page for all VMs.
type Dashboard struct {
	mgr    *vm.Manager
	mu     sync.RWMutex
	latest map[string]*VMStats
}

func NewDashboard(mgr *vm.Manager) *Dashboard {
	return &Dashboard{mgr: mgr, latest: make(map[string]*VMStats)}
}

// Poll refreshes stats for every running VM every interval until ctx is done.
func (d *Dashboard) Poll(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refresh()
		}
	}
}

func (d *Dashboard) refresh() {
	entries, err := d.mgr.List()
	if err != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range entries {
		vs := &VMStats{
			Name:         e.Config.Name,
			Template:     e.Config.Template,
			Running:      e.State.Running,
			InstallState: e.State.InstallState,
			PID:          e.State.PID,
		}
		if e.State.Running && e.State.QMPSocket != "" {
			client, err := qemu.NewQMPClient(e.State.QMPSocket, 2*time.Second)
			if err == nil {
				if raw, err := client.QueryRaw(); err == nil {
					vs.Stats = monitor.Stats{
						MemActual:      raw.BalloonActual,
						DiskReadBytes:  raw.DiskRdBytes,
						DiskWriteBytes: raw.DiskWrBytes,
						NetRxBytes:     raw.NetRxBytes,
						NetTxBytes:     raw.NetTxBytes,
					}
				}
				_ = client.Close()
			}
		}
		d.latest[e.Config.Name] = vs
	}
}

func (d *Dashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/stats" {
		d.mu.RLock()
		list := make([]*VMStats, 0, len(d.latest))
		for _, v := range d.latest {
			list = append(list, v)
		}
		d.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, dashboardHTML)
}

// Listen starts the HTTP server on addr, blocking until ctx is cancelled.
func (d *Dashboard) Listen(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: d}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

const dashboardHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>vee dashboard</title>
<meta http-equiv="refresh" content="2">
<style>
body{font-family:monospace;background:#0d0d0d;color:#e0e0e0;padding:2rem}
h1{color:#5af}
table{border-collapse:collapse;width:100%}
th,td{padding:.4rem .8rem;text-align:left;border-bottom:1px solid #222}
th{color:#888}
.running{color:#5f5}
.stopped{color:#f55}
.installing{color:#ff5}
</style>
</head>
<body>
<h1>vee dashboard</h1>
<div id="app">loading…</div>
<script>
async function load(){
  const r=await fetch('/api/stats');
  const vms=await r.json();
  if(!vms||!vms.length){document.getElementById('app').textContent='No VMs found.';return;}
  const fmt=b=>{if(b>=1<<30)return(b/(1<<30)).toFixed(1)+'G';if(b>=1<<20)return(b/(1<<20)).toFixed(1)+'M';if(b>=1<<10)return(b/(1<<10)).toFixed(1)+'K';return b+'B'};
  let h='<table><thead><tr><th>Name</th><th>Template</th><th>Status</th><th>PID</th><th>Mem</th><th>Disk R/W</th><th>Net Rx/Tx</th></tr></thead><tbody>';
  for(const v of vms){
    const s=v.install_state==='pending'?'<span class="installing">installing</span>':v.running?'<span class="running">running</span>':'<span class="stopped">stopped</span>';
    const st=v.stats||{};
    h+=` + "`" + `<tr><td>${v.name}</td><td>${v.template}</td><td>${s}</td><td>${v.pid||'-'}</td><td>${fmt(st.mem_actual||0)}</td><td>${fmt(st.disk_read_bytes||0)}/${fmt(st.disk_write_bytes||0)}</td><td>${fmt(st.net_rx_bytes||0)}/${fmt(st.net_tx_bytes||0)}</td></tr>` + "`" + `;
  }
  h+='</tbody></table>';
  document.getElementById('app').innerHTML=h;
}
load();
</script>
</body>
</html>`
