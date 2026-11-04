// Command dockermetrics is a small, pinned, native container-resource
// exporter for the optional monitoring "resources" Compose profile. It reads
// the Docker Engine API through a read-only socket mount and exposes
// per-container CPU, memory and network metrics plus Docker disk usage. It
// also writes the Prometheus file-SD entries for itself and node-exporter and
// removes them on shutdown, so the optional targets appear and disappear with
// the profile.
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

func main() {
	socket := os.Getenv("DOCKER_SOCKET")
	if socket == "" {
		socket = "/var/run/docker.sock"
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
	exporter := &dockerExporter{client: client, socket: socket}
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
	log.Printf("dockermetrics listening on %s (socket %s)", listenAddr, socket)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go exporter.poll(ctx, 15*time.Second)
	<-ctx.Done()
	removeSD()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
}

type containerStats struct {
	ID       string
	Name     string
	Service  string
	Project  string
	State    string
	CPUTotal float64
	CPUCores float64
	MemUsed  float64
	MemLimit float64
	RxBytes  float64
	TxBytes  float64
}

type snapshot struct {
	up          atomic.Bool
	lastSuccess atomic.Int64 // unix seconds
	containers  atomic.Pointer[[]containerStats]
	diskUsage   atomic.Pointer[map[string]float64]
}

type dockerExporter struct {
	client *http.Client
	socket string
	state  snapshot
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

func (d *dockerExporter) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker api %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

func (d *dockerExporter) collect(ctx context.Context) {
	pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var containers []struct {
		Id     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Labels map[string]string `json:"Labels"`
		State  string            `json:"State"`
	}
	if err := d.get(pollCtx, "/containers/json?all=1", &containers); err != nil {
		d.state.up.Store(false)
		log.Printf("container list failed: %v", err)
		return
	}
	stats := make([]containerStats, 0, len(containers))
	for _, container := range containers {
		name := container.Id
		if len(container.Names) > 0 {
			name = container.Names[0][1:] // leading slash
		}
		entry := containerStats{
			ID:      container.Id[:12],
			Name:    name,
			Service: container.Labels["com.docker.compose.service"],
			Project: container.Labels["com.docker.compose.project"],
			State:   container.State,
		}
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
		if err := d.get(pollCtx, "/containers/"+container.Id+"/stats?stream=false&one-shot=true", &sample); err != nil {
			log.Printf("stats for %s failed: %v", entry.Name, err)
			continue
		}
		entry.CPUTotal = sample.CPUStats.CPUUsage.TotalUsage / 1e9
		entry.CPUCores = sample.CPUStats.OnlineCPUs
		entry.MemUsed = sample.MemoryStats.Usage
		if inactive := sample.MemoryStats.Stats.InactiveFile; inactive > 0 {
			entry.MemUsed -= inactive
		} else if inactive := sample.MemoryStats.Stats.TotalInactiveFile; inactive > 0 {
			entry.MemUsed -= inactive
		}
		entry.MemLimit = sample.MemoryStats.Limit
		for _, network := range sample.Networks {
			entry.RxBytes += network.RxBytes
			entry.TxBytes += network.TxBytes
		}
		stats = append(stats, entry)
	}
	var df struct {
		LayersSize float64 `json:"LayersSize"`
		Images     []struct {
			Size float64 `json:"Size"`
		} `json:"Images"`
		Containers []struct {
			SizeRw float64 `json:"SizeRw"`
		} `json:"Containers"`
		Volumes []struct {
			UsageData struct {
				Size float64 `json:"Size"`
			} `json:"UsageData"`
		} `json:"Volumes"`
	}
	if err := d.get(pollCtx, "/system/df", &df); err != nil {
		d.state.up.Store(false)
		log.Printf("system df failed: %v", err)
		return
	}
	disk := map[string]float64{"images": 0, "containers": 0, "volumes": 0}
	for _, image := range df.Images {
		disk["images"] += image.Size
	}
	for _, container := range df.Containers {
		disk["containers"] += container.SizeRw
	}
	for _, volume := range df.Volumes {
		disk["volumes"] += volume.UsageData.Size
	}
	d.state.containers.Store(&stats)
	d.state.diskUsage.Store(&disk)
	d.state.lastSuccess.Store(time.Now().Unix())
	d.state.up.Store(true)
}

var (
	cpuDesc         = prometheus.NewDesc("jamshid_container_cpu_seconds_total", "Container cumulative CPU seconds.", []string{"service", "container", "state"}, nil)
	coresDesc       = prometheus.NewDesc("jamshid_container_cpu_cores", "Container online CPU cores.", []string{"service", "container", "state"}, nil)
	memDesc         = prometheus.NewDesc("jamshid_container_memory_used_bytes", "Container memory usage excluding page cache.", []string{"service", "container", "state"}, nil)
	limitDesc       = prometheus.NewDesc("jamshid_container_memory_limit_bytes", "Container memory limit.", []string{"service", "container", "state"}, nil)
	rxDesc          = prometheus.NewDesc("jamshid_container_network_rx_bytes_total", "Container network bytes received.", []string{"service", "container", "state"}, nil)
	txDesc          = prometheus.NewDesc("jamshid_container_network_tx_bytes_total", "Container network bytes transmitted.", []string{"service", "container", "state"}, nil)
	diskDesc        = prometheus.NewDesc("jamshid_docker_disk_usage_bytes", "Docker disk usage by scope.", []string{"scope"}, nil)
	upDesc          = prometheus.NewDesc("jamshid_docker_exporter_up", "1 when the last Docker API poll succeeded.", nil, nil)
	lastSuccessDesc = prometheus.NewDesc("jamshid_docker_exporter_last_success_timestamp_seconds", "Unix timestamp of the last successful Docker API poll.", nil, nil)
)

func (d *dockerExporter) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{cpuDesc, coresDesc, memDesc, limitDesc, rxDesc, txDesc, diskDesc, upDesc, lastSuccessDesc} {
		ch <- desc
	}
}

func (d *dockerExporter) Collect(ch chan<- prometheus.Metric) {
	up := 0.0
	if d.state.up.Load() {
		up = 1
	}
	ch <- prometheus.MustNewConstMetric(upDesc, prometheus.GaugeValue, up)
	ch <- prometheus.MustNewConstMetric(lastSuccessDesc, prometheus.GaugeValue, float64(d.state.lastSuccess.Load()))
	if containers := d.state.containers.Load(); containers != nil {
		for _, container := range *containers {
			labels := []string{container.Service, container.Name, container.State}
			ch <- prometheus.MustNewConstMetric(cpuDesc, prometheus.CounterValue, container.CPUTotal, labels...)
			if container.CPUCores > 0 {
				ch <- prometheus.MustNewConstMetric(coresDesc, prometheus.GaugeValue, container.CPUCores, labels...)
			}
			ch <- prometheus.MustNewConstMetric(memDesc, prometheus.GaugeValue, container.MemUsed, labels...)
			if container.MemLimit > 0 {
				ch <- prometheus.MustNewConstMetric(limitDesc, prometheus.GaugeValue, container.MemLimit, labels...)
			}
			ch <- prometheus.MustNewConstMetric(rxDesc, prometheus.CounterValue, container.RxBytes, labels...)
			ch <- prometheus.MustNewConstMetric(txDesc, prometheus.CounterValue, container.TxBytes, labels...)
		}
	}
	if disk := d.state.diskUsage.Load(); disk != nil {
		for scope, value := range *disk {
			ch <- prometheus.MustNewConstMetric(diskDesc, prometheus.GaugeValue, value, scope)
		}
	}
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
