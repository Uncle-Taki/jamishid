// Command dockermetrics is a small, pinned, native container-resource
// exporter for the optional monitoring "resources" Compose profile. It reads
// the Docker Engine API through a read-only socket mount and exposes
// per-container CPU, memory and network metrics plus host-wide Docker disk
// usage. It also writes the Prometheus file-SD entries for itself and
// node-exporter and removes them on shutdown, so the optional targets appear
// and disappear with the profile.
//
// By default only containers of the configured Compose project
// (JAMSHID_DOCKER_PROJECT, default "jamshid") are exported, each carrying its
// project label, so unrelated applications on the host and same-named
// services in other projects are never mixed. JAMSHID_DOCKER_PROJECT=all
// disables the filter. Docker /system/df values are host-wide by nature and
// are labeled as such; image layers are counted once via LayersSize instead
// of summing per-image sizes, which double counts shared layers.
//
// Failed per-container stats requests do not fail the poll (the Docker API
// itself is reachable) but are exported as jamshid_docker_stats_errors_total
// and jamshid_docker_stats_failed so dashboards and rules can see partial
// collection failures.
//
// The Docker socket is root-equivalent: this binary must only run in the
// explicitly documented optional resources profile, never in the normal
// application stack.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	listenAddr = ":9303"
	sdPath     = "/sd/resources.json"
)

// dockerAPI is the slice of the Docker Engine API the exporter reads. The
// interface exists so tests can exercise collection without a Docker daemon.
type dockerAPI interface {
	listContainers(ctx context.Context) ([]containerInfo, error)
	containerStats(ctx context.Context, id string) (*statsSample, error)
	systemDF(ctx context.Context) (*dfSample, error)
}

type containerInfo struct {
	ID      string
	Name    string
	Service string
	Project string
	State   string
}

type statsSample struct {
	CPUTotal float64
	CPUCores float64
	MemUsed  float64
	MemLimit float64
	RxBytes  float64
	TxBytes  float64
}

// dfSample reports host-wide Docker disk usage. LayersSize counts every
// image layer byte once; per-image Sizes would double count shared layers.
type dfSample struct {
	LayersSize   float64
	ContainersRW float64
	Volumes      float64
}

// httpAPI talks to the Docker Engine over the unix socket.
type httpAPI struct {
	client *http.Client
}

func (h *httpAPI) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker api %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

func (h *httpAPI) listContainers(ctx context.Context) ([]containerInfo, error) {
	var raw []struct {
		Id     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Labels map[string]string `json:"Labels"`
		State  string            `json:"State"`
	}
	if err := h.get(ctx, "/containers/json?all=1", &raw); err != nil {
		return nil, err
	}
	result := make([]containerInfo, 0, len(raw))
	for _, container := range raw {
		name := container.Id
		if len(container.Names) > 0 {
			name = container.Names[0][1:] // leading slash
		}
		result = append(result, containerInfo{
			ID:      container.Id,
			Name:    name,
			Service: container.Labels["com.docker.compose.service"],
			Project: container.Labels["com.docker.compose.project"],
			State:   container.State,
		})
	}
	return result, nil
}

func (h *httpAPI) containerStats(ctx context.Context, id string) (*statsSample, error) {
	var sample struct {
		CPUStats struct {
			CPUUsage struct {
				TotalUsage float64 `json:"total_usage"`
			} `json:"cpu_usage"`
			OnlineCPUs float64 `json:"online_cpus"`
		} `json:"cpu_stats"`
		MemoryStats struct {
			Usage float64 `json:"usage"`
			Limit float64 `json:"limit"`
			Stats struct {
				InactiveFile      float64 `json:"inactive_file"`
				TotalInactiveFile float64 `json:"total_inactive_file"`
			} `json:"stats"`
		} `json:"memory_stats"`
		Networks map[string]struct {
			RxBytes float64 `json:"rx_bytes"`
			TxBytes float64 `json:"tx_bytes"`
		} `json:"networks"`
	}
	if err := h.get(ctx, "/containers/"+id+"/stats?stream=false&one-shot=true", &sample); err != nil {
		return nil, err
	}
	result := &statsSample{
		CPUTotal: sample.CPUStats.CPUUsage.TotalUsage / 1e9,
		CPUCores: sample.CPUStats.OnlineCPUs,
		MemUsed:  sample.MemoryStats.Usage,
		MemLimit: sample.MemoryStats.Limit,
	}
	if inactive := sample.MemoryStats.Stats.InactiveFile; inactive > 0 {
		result.MemUsed -= inactive
	} else if inactive := sample.MemoryStats.Stats.TotalInactiveFile; inactive > 0 {
		result.MemUsed -= inactive
	}
	for _, network := range sample.Networks {
		result.RxBytes += network.RxBytes
		result.TxBytes += network.TxBytes
	}
	return result, nil
}

func (h *httpAPI) systemDF(ctx context.Context) (*dfSample, error) {
	var df struct {
		LayersSize float64 `json:"LayersSize"`
		Containers []struct {
			SizeRw float64 `json:"SizeRw"`
		} `json:"Containers"`
		Volumes []struct {
			UsageData struct {
				Size float64 `json:"Size"`
			} `json:"UsageData"`
		} `json:"Volumes"`
	}
	if err := h.get(ctx, "/system/df", &df); err != nil {
		return nil, err
	}
	result := &dfSample{LayersSize: df.LayersSize}
	for _, container := range df.Containers {
		result.ContainersRW += container.SizeRw
	}
	for _, volume := range df.Volumes {
		result.Volumes += volume.UsageData.Size
	}
	return result, nil
}

// containerStats is one exported container sample.
type containerStats struct {
	containerInfo
	stats statsSample
}

// pollState is one immutable collection result.
type pollState struct {
	up          bool
	lastSuccess int64
	containers  []containerStats
	states      []containerInfo
	disk        map[string]float64
	// statsErrors accumulates failed per-container stats requests by container
	// name across polls; statsFailed counts the failures of the last poll.
	statsErrors map[string]float64
	statsFailed int
}

type dockerExporter struct {
	api     dockerAPI
	project string
	state   atomic.Pointer[pollState]
}

func (d *dockerExporter) poll(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		d.collect(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *dockerExporter) collect(ctx context.Context) {
	pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	containers, err := d.api.listContainers(pollCtx)
	if err != nil {
		d.markFailed()
		log.Printf("container list failed: %v", err)
		return
	}
	previous := d.state.Load()
	statsErrors := map[string]float64{}
	stats := make([]containerStats, 0, len(containers))
	states := make([]containerInfo, 0, len(containers))
	failed := 0
	for _, container := range containers {
		if d.project != "all" && container.Project != d.project {
			continue
		}
		states = append(states, container)
		// Stopped containers have no live resource sample; their state is
		// exported separately rather than fabricated as zero usage or an error.
		if container.State != "running" {
			continue
		}
		if previous != nil {
			if count := previous.statsErrors[container.Name]; count > 0 {
				statsErrors[container.Name] = count
			}
		}
		sample, err := d.api.containerStats(pollCtx, container.ID)
		if err != nil {
			// A stopped container or a busy daemon must not fail the whole
			// poll, but the partial failure stays visible.
			failed++
			statsErrors[container.Name]++
			log.Printf("stats for %s failed: %v", container.Name, err)
			continue
		}
		stats = append(stats, containerStats{containerInfo: container, stats: *sample})
	}
	df, err := d.api.systemDF(pollCtx)
	if err != nil {
		d.markFailed()
		log.Printf("system df failed: %v", err)
		return
	}
	disk := map[string]float64{
		"image_layers_unique": df.LayersSize,
		"containers_rw":       df.ContainersRW,
		"volumes":             df.Volumes,
	}
	d.state.Store(&pollState{
		up:          true,
		lastSuccess: time.Now().Unix(),
		containers:  stats,
		states:      states,
		disk:        disk,
		statsErrors: statsErrors,
		statsFailed: failed,
	})
}

func (d *dockerExporter) markFailed() {
	next := &pollState{}
	if old := d.state.Load(); old != nil {
		*next = *old
	}
	next.up = false
	d.state.Store(next)
}

var (
	containerInfoDesc = prometheus.NewDesc("jamshid_container_info", "Observed container state, including stopped containers without live resource samples.", []string{"project", "service", "container", "state"}, nil)
	cpuDesc           = prometheus.NewDesc("jamshid_container_cpu_seconds_total", "Container cumulative CPU seconds.", []string{"project", "service", "container", "state"}, nil)
	coresDesc         = prometheus.NewDesc("jamshid_container_cpu_cores", "Container online CPU cores.", []string{"project", "service", "container", "state"}, nil)
	memDesc           = prometheus.NewDesc("jamshid_container_memory_used_bytes", "Container memory usage excluding page cache.", []string{"project", "service", "container", "state"}, nil)
	limitDesc         = prometheus.NewDesc("jamshid_container_memory_limit_bytes", "Container memory limit.", []string{"project", "service", "container", "state"}, nil)
	rxDesc            = prometheus.NewDesc("jamshid_container_network_rx_bytes_total", "Container network bytes received.", []string{"project", "service", "container", "state"}, nil)
	txDesc            = prometheus.NewDesc("jamshid_container_network_tx_bytes_total", "Container network bytes transmitted.", []string{"project", "service", "container", "state"}, nil)
	diskDesc          = prometheus.NewDesc("jamshid_docker_disk_usage_bytes", "Host-wide Docker disk usage by scope; image layers are shared and counted once.", []string{"scope"}, nil)
	upDesc            = prometheus.NewDesc("jamshid_docker_exporter_up", "1 when the last Docker API poll (container list and system df) succeeded.", nil, nil)
	lastSuccessDesc   = prometheus.NewDesc("jamshid_docker_exporter_last_success_timestamp_seconds", "Unix timestamp of the last successful Docker API poll.", nil, nil)
	statsErrorsDesc   = prometheus.NewDesc("jamshid_docker_stats_errors_total", "Failed per-container stats requests by container name; partial collection failures that do not fail the poll.", []string{"container"}, nil)
	statsFailedDesc   = prometheus.NewDesc("jamshid_docker_stats_failed", "Containers whose stats request failed in the last poll.", nil, nil)
)

func (d *dockerExporter) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{containerInfoDesc, cpuDesc, coresDesc, memDesc, limitDesc, rxDesc, txDesc, diskDesc, upDesc, lastSuccessDesc, statsErrorsDesc, statsFailedDesc} {
		ch <- desc
	}
}

func (d *dockerExporter) Collect(ch chan<- prometheus.Metric) {
	state := d.state.Load()
	up := 0.0
	if state != nil && state.up {
		up = 1
	}
	ch <- prometheus.MustNewConstMetric(upDesc, prometheus.GaugeValue, up)
	if state == nil {
		return
	}
	for _, container := range state.states {
		ch <- prometheus.MustNewConstMetric(containerInfoDesc, prometheus.GaugeValue, 1, container.Project, container.Service, container.Name, container.State)
	}
	ch <- prometheus.MustNewConstMetric(lastSuccessDesc, prometheus.GaugeValue, float64(state.lastSuccess))
	ch <- prometheus.MustNewConstMetric(statsFailedDesc, prometheus.GaugeValue, float64(state.statsFailed))
	for name, count := range state.statsErrors {
		ch <- prometheus.MustNewConstMetric(statsErrorsDesc, prometheus.CounterValue, count, name)
	}
	for _, container := range state.containers {
		labels := []string{container.Project, container.Service, container.Name, container.State}
		ch <- prometheus.MustNewConstMetric(cpuDesc, prometheus.CounterValue, container.stats.CPUTotal, labels...)
		if container.stats.CPUCores > 0 {
			ch <- prometheus.MustNewConstMetric(coresDesc, prometheus.GaugeValue, container.stats.CPUCores, labels...)
		}
		ch <- prometheus.MustNewConstMetric(memDesc, prometheus.GaugeValue, container.stats.MemUsed, labels...)
		if container.stats.MemLimit > 0 {
			ch <- prometheus.MustNewConstMetric(limitDesc, prometheus.GaugeValue, container.stats.MemLimit, labels...)
		}
		ch <- prometheus.MustNewConstMetric(rxDesc, prometheus.CounterValue, container.stats.RxBytes, labels...)
		ch <- prometheus.MustNewConstMetric(txDesc, prometheus.CounterValue, container.stats.TxBytes, labels...)
	}
	for scope, value := range state.disk {
		ch <- prometheus.MustNewConstMetric(diskDesc, prometheus.GaugeValue, value, scope)
	}
}

func main() {
	socket := os.Getenv("DOCKER_SOCKET")
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	project := os.Getenv("JAMSHID_DOCKER_PROJECT")
	if project == "" {
		project = "jamshid"
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
		Timeout: 10 * time.Second,
	}
	exporter := &dockerExporter{api: &httpAPI{client: client}, project: project}
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), exporter)

	// Advertise the optional resource targets; removed again on shutdown.
	writeSD()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	server := &http.Server{Addr: listenAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	log.Printf("dockermetrics listening on %s (socket %s, project %s)", listenAddr, socket, project)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go exporter.poll(ctx, 15*time.Second)
	<-ctx.Done()
	removeSD()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
}

// writeSD advertises the optional resource scrape targets through Prometheus
// file service discovery. One file per job so each exporter is scraped by
// exactly one job and no series are duplicated.
func writeSD() {
	sd := map[string][]map[string]any{
		"node-exporter.json": {
			{"targets": []string{"node-exporter:9100"}, "labels": map[string]string{"job": "node-exporter"}},
		},
		"docker-exporter.json": {
			{"targets": []string{"docker-exporter:9303"}, "labels": map[string]string{"job": "docker-exporter"}},
		},
	}
	for name, groups := range sd {
		raw, err := json.Marshal(groups)
		if err != nil {
			log.Printf("sd marshal: %v", err)
			continue
		}
		path := filepath.Join(filepath.Dir(sdPath), name)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			log.Printf("sd write (optional): %v", err)
			continue
		}
		log.Printf("wrote prometheus file_sd %s", path)
	}
}

func removeSD() {
	for _, name := range []string{"node-exporter.json", "docker-exporter.json"} {
		path := filepath.Join(filepath.Dir(sdPath), name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("sd remove: %v", err)
			continue
		}
	}
	log.Printf("removed prometheus file_sd entries")
}
