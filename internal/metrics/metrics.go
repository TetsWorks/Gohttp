package metrics

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TetsWorks/Gohttp/internal/parser"
	"github.com/TetsWorks/Gohttp/internal/router"
)

type Collector struct {
	mu             sync.RWMutex
	totalRequests  int64
	totalErrors    int64
	activeConns    int64
	bytesIn        int64
	bytesOut       int64
	routes         map[string]*RouteMetrics
	latencyBuckets []int64
	latencyBounds  []float64
	reqWindow      []int64
	windowMu       sync.Mutex
	startTime      time.Time
}

type RouteMetrics struct {
	Method string; Path string
	Requests int64; Errors int64
	TotalLatency int64; MinLatency int64; MaxLatency int64
	StatusCodes map[int]int64
	mu sync.Mutex
}

func (rm *RouteMetrics) avg() float64 {
	if rm.Requests == 0 { return 0 }
	return float64(rm.TotalLatency) / float64(rm.Requests) / 1000
}

func New() *Collector {
	return &Collector{
		routes: make(map[string]*RouteMetrics),
		latencyBounds: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
		latencyBuckets: make([]int64, 13),
		startTime: time.Now(),
	}
}

func (c *Collector) Record(method, path string, statusCode int, latency time.Duration, bytesIn, bytesOut int64) {
	atomic.AddInt64(&c.totalRequests, 1)
	atomic.AddInt64(&c.bytesIn, bytesIn)
	atomic.AddInt64(&c.bytesOut, bytesOut)
	if statusCode >= 500 { atomic.AddInt64(&c.totalErrors, 1) }

	latMs := float64(latency.Microseconds()) / 1000
	c.mu.Lock()
	added := false
	for i, bound := range c.latencyBounds {
		if latMs <= bound { c.latencyBuckets[i]++; added = true; break }
	}
	if !added { c.latencyBuckets[len(c.latencyBuckets)-1]++ }
	c.mu.Unlock()

	c.windowMu.Lock()
	now := time.Now().UnixNano()
	cutoff := now - int64(60*time.Second)
	filtered := c.reqWindow[:0]
	for _, t := range c.reqWindow { if t > cutoff { filtered = append(filtered, t) } }
	c.reqWindow = append(filtered, now)
	c.windowMu.Unlock()

	key := method + " " + path
	c.mu.Lock()
	rm, ok := c.routes[key]
	if !ok {
		rm = &RouteMetrics{Method: method, Path: path, MinLatency: math.MaxInt64, StatusCodes: make(map[int]int64)}
		c.routes[key] = rm
	}
	c.mu.Unlock()
	rm.mu.Lock()
	rm.Requests++; rm.StatusCodes[statusCode]++
	latUs := latency.Microseconds()
	rm.TotalLatency += latUs
	if latUs < rm.MinLatency { rm.MinLatency = latUs }
	if latUs > rm.MaxLatency { rm.MaxLatency = latUs }
	if statusCode >= 500 { rm.Errors++ }
	rm.mu.Unlock()
}

func (c *Collector) IncrConns() { atomic.AddInt64(&c.activeConns, 1) }
func (c *Collector) DecrConns() { atomic.AddInt64(&c.activeConns, -1) }

func (c *Collector) ReqPerSec() float64 {
	c.windowMu.Lock(); defer c.windowMu.Unlock()
	if len(c.reqWindow) == 0 { return 0 }
	cutoff := time.Now().UnixNano() - int64(10*time.Second)
	count := 0
	for _, t := range c.reqWindow { if t > cutoff { count++ } }
	return float64(count) / 10.0
}

type Snapshot struct {
	Uptime        string          `json:"uptime"`
	TotalRequests int64           `json:"total_requests"`
	TotalErrors   int64           `json:"total_errors"`
	ActiveConns   int64           `json:"active_connections"`
	ReqPerSec     float64         `json:"req_per_sec"`
	BytesIn       int64           `json:"bytes_in"`
	BytesOut      int64           `json:"bytes_out"`
	Routes        []RouteSnapshot `json:"routes"`
	Latency       LatencySnapshot `json:"latency"`
}

type RouteSnapshot struct {
	Method string; Path string
	Requests int64; Errors int64
	AvgLatency float64 `json:"avg_latency_ms"`
	MinLatency float64 `json:"min_latency_ms"`
	MaxLatency float64 `json:"max_latency_ms"`
	StatusCodes map[int]int64 `json:"status_codes"`
}

type LatencySnapshot struct {
	Bounds  []float64 `json:"bounds_ms"`
	Buckets []int64   `json:"buckets"`
}

func (c *Collector) Snapshot() Snapshot {
	c.mu.RLock(); defer c.mu.RUnlock()
	snap := Snapshot{
		Uptime: time.Since(c.startTime).Round(time.Second).String(),
		TotalRequests: atomic.LoadInt64(&c.totalRequests),
		TotalErrors:   atomic.LoadInt64(&c.totalErrors),
		ActiveConns:   atomic.LoadInt64(&c.activeConns),
		ReqPerSec:     c.ReqPerSec(),
		BytesIn:       atomic.LoadInt64(&c.bytesIn),
		BytesOut:      atomic.LoadInt64(&c.bytesOut),
		Latency: LatencySnapshot{Bounds: c.latencyBounds, Buckets: append([]int64{}, c.latencyBuckets...)},
	}
	for _, rm := range c.routes {
		rm.mu.Lock()
		rs := RouteSnapshot{Method: rm.Method, Path: rm.Path, Requests: rm.Requests,
			Errors: rm.Errors, AvgLatency: rm.avg(), StatusCodes: make(map[int]int64)}
		if rm.Requests > 0 {
			rs.MinLatency = float64(rm.MinLatency)/1000; rs.MaxLatency = float64(rm.MaxLatency)/1000
		}
		for k, v := range rm.StatusCodes { rs.StatusCodes[k] = v }
		rm.mu.Unlock()
		snap.Routes = append(snap.Routes, rs)
	}
	sort.Slice(snap.Routes, func(i, j int) bool { return snap.Routes[i].Requests > snap.Routes[j].Requests })
	return snap
}

type trackingWriter struct {
	parser.ResponseWriter
	statusCode int
	bytes      int64
}
func (t *trackingWriter) WriteHeader(code int) { t.statusCode = code; t.ResponseWriter.WriteHeader(code) }
func (t *trackingWriter) Write(data []byte) (int, error) {
	n, err := t.ResponseWriter.Write(data); t.bytes += int64(n); return n, err
}
func (t *trackingWriter) Status() int { if t.statusCode == 0 { return 200 }; return t.statusCode }

func (c *Collector) Middleware() router.Middleware {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(w parser.ResponseWriter, r *parser.Request) {
			start := time.Now()
			tw := &trackingWriter{ResponseWriter: w}
			next(tw, r)
			c.Record(r.Method, r.Path, tw.Status(), time.Since(start), r.ContentLen, tw.bytes)
		}
	}
}

func (c *Collector) DashboardHandler() router.HandlerFunc {
	return func(w parser.ResponseWriter, r *parser.Request) {
		if strings.Contains(r.Path, "json") || r.Headers.Get("Accept") == "application/json" {
			w.JSON(200, c.Snapshot()); return
		}
		w.HTML(200, c.dashboardHTML())
	}
}

func (c *Collector) dashboardHTML() string {
	snap := c.Snapshot()
	data, _ := json.Marshal(snap)
	var rows strings.Builder
	for _, rt := range snap.Routes {
		er := 0.0
		if rt.Requests > 0 { er = float64(rt.Errors) / float64(rt.Requests) * 100 }
		rows.WriteString(fmt.Sprintf(`<tr><td><span class="badge %s">%s</span></td><td class=path>%s</td><td>%d</td><td>%.1f%%</td><td>%.2fms</td><td>%.2fms</td><td>%.2fms</td></tr>`,
			rt.Method, rt.Method, rt.Path, rt.Requests, er, rt.AvgLatency, rt.MinLatency, rt.MaxLatency))
	}
	return fmt.Sprintf(`<!DOCTYPE html><html><head><meta charset=utf-8>
<title>GoHTTP Metrics</title>
<style>:root{--bg:#0f0f23;--card:#1a1a35;--border:#2a2a4a;--text:#e0e0ff;--muted:#888;--blue:#00d2ff;--green:#00ff88;--red:#ff4444;--yellow:#ffd700;--purple:#b44fff}
*{box-sizing:border-box;margin:0;padding:0}body{background:var(--bg);color:var(--text);font-family:monospace;padding:1.5rem}
h1{font-size:1.8rem;background:linear-gradient(90deg,var(--blue),var(--purple));-webkit-background-clip:text;-webkit-text-fill-color:transparent;margin-bottom:1.5rem}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:1rem;margin-bottom:1.5rem}
.card{background:var(--card);border:1px solid var(--border);border-radius:8px;padding:1rem}
.label{color:var(--muted);font-size:.7rem;text-transform:uppercase;letter-spacing:.1em}
.value{font-size:1.6rem;font-weight:700;color:var(--blue);margin:.2rem 0}
canvas{width:100%%;height:180px;display:block;background:var(--card);border:1px solid var(--border);border-radius:8px;margin-bottom:1.5rem}
table{width:100%%;border-collapse:collapse;background:var(--card);border:1px solid var(--border);border-radius:8px;overflow:hidden}
th{background:#111130;padding:.7rem 1rem;text-align:left;font-size:.7rem;text-transform:uppercase;color:var(--muted)}
td{padding:.6rem 1rem;border-bottom:1px solid var(--border);font-size:.85rem}
tr:last-child td{border:none}tr:hover td{background:rgba(0,210,255,.04)}
.badge{padding:.2rem .5rem;border-radius:4px;font-size:.7rem;font-weight:700}
.GET{background:#0d3d6e;color:var(--blue)}.POST{background:#0d4a1e;color:var(--green)}
.PUT{background:#4a3d00;color:var(--yellow)}.DELETE{background:#4a1010;color:var(--red)}
.PATCH{background:#3d1060;color:var(--purple)}.path{font-family:monospace}
</style></head><body>
<h1>⚡ GoHTTP Dashboard</h1>
<div class=grid>
<div class=card><div class=label>Total Requests</div><div class=value>%d</div></div>
<div class=card><div class=label>Req/s (10s)</div><div class=value>%.2f</div></div>
<div class=card><div class=label>Erros 5xx</div><div class=value style="color:var(--red)">%d</div></div>
<div class=card><div class=label>Conexões</div><div class=value>%d</div></div>
<div class=card><div class=label>Uptime</div><div class=value style="color:var(--green);font-size:1rem">%s</div></div>
<div class=card><div class=label>In/Out</div><div class=value style="font-size:.9rem">%s / %s</div></div>
</div>
<canvas id=c></canvas>
<table><thead><tr><th>Método</th><th>Path</th><th>Req</th><th>Err%%</th><th>Avg</th><th>Min</th><th>Max</th></tr></thead>
<tbody>%s</tbody></table>
<script>
const d=%s;
const cv=document.getElementById('c'),ctx=cv.getContext('2d');
cv.width=cv.offsetWidth;cv.height=180;
const bk=d.latency.buckets,bn=d.latency.bounds_ms,mx=Math.max(...bk,1);
const w=cv.width/bk.length;
ctx.fillStyle='#1a1a35';ctx.fillRect(0,0,cv.width,cv.height);
bk.forEach((v,i)=>{
  const h=(v/mx)*150,x=i*w,y=160-h,p=i/bk.length;
  ctx.fillStyle='hsl('+(120+p*120)+',80%%,50%%)';
  ctx.fillRect(x+2,y,w-4,h);
  if(bn[i]){ctx.fillStyle='#666';ctx.font='9px monospace';ctx.fillText(bn[i],x+1,175);}
});
ctx.fillStyle='#888';ctx.font='11px monospace';ctx.fillText('Latência (ms)',8,14);
setTimeout(()=>location.reload(),5000);
</script></body></html>`,
		snap.TotalRequests, snap.ReqPerSec, snap.TotalErrors, snap.ActiveConns, snap.Uptime,
		formatBytes(snap.BytesIn), formatBytes(snap.BytesOut), rows.String(), string(data))
}

func formatBytes(n int64) string {
	switch { case n>=1<<30: return fmt.Sprintf("%.1fGB",float64(n)/(1<<30))
	case n>=1<<20: return fmt.Sprintf("%.1fMB",float64(n)/(1<<20))
	case n>=1<<10: return fmt.Sprintf("%.1fKB",float64(n)/(1<<10))
	default: return fmt.Sprintf("%dB",n) }
}
