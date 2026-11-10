package main

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// fakeAPI serves canned Docker Engine API responses.
type fakeAPI struct {
	containers []containerInfo
	statsByID  map[string]*statsSample
	statsErr   map[string]error
	df         *dfSample
	listErr    error
}

func (f *fakeAPI) listContainers(context.Context) ([]containerInfo, error) {
	return f.containers, f.listErr
}

func (f *fakeAPI) containerStats(_ context.Context, id string) (*statsSample, error) {
	if err, ok := f.statsErr[id]; ok {
		return nil, err
	}
	sample, ok := f.statsByID[id]
	if !ok {
		return nil, errors.New("no sample")
	}
	return sample, nil
}

func (f *fakeAPI) systemDF(context.Context) (*dfSample, error) {
	return f.df, nil
}

func collect(t *testing.T, e *dockerExporter) map[string]*dto.MetricFamily {
	t.Helper()
	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(e); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]*dto.MetricFamily{}
	for _, family := range families {
		result[family.GetName()] = family
	}
	return result
}

func gauge(t *testing.T, families map[string]*dto.MetricFamily, name string) float64 {
	t.Helper()
	family, ok := families[name]
	if !ok {
		t.Fatalf("metric %s missing", name)
	}
	if len(family.GetMetric()) != 1 {
		t.Fatalf("metric %s: want 1 series, got %d", name, len(family.GetMetric()))
	}
	return family.GetMetric()[0].GetGauge().GetValue()
}

func labelsOf(metric *dto.Metric) map[string]string {
	result := map[string]string{}
	for _, label := range metric.GetLabel() {
		result[label.GetName()] = label.GetValue()
	}
	return result
}

// TestProjectScopingKeepsOtherProjectsOut: two Compose projects sharing the
// same service name must never be mixed; only the configured project's
// containers are exported and every series carries its project label
// (review finding R6).
func TestProjectScopingKeepsOtherProjectsOut(t *testing.T) {
	api := &fakeAPI{
		containers: []containerInfo{
			{ID: "a", Name: "jamshid-backend-1", Service: "backend", Project: "jamshid", State: "running"},
			{ID: "b", Name: "other-backend-1", Service: "backend", Project: "other-project", State: "running"},
			{ID: "c", Name: "unrelated", Service: "", Project: "", State: "running"},
		},
		statsByID: map[string]*statsSample{
			"a": {CPUTotal: 1, MemUsed: 10, MemLimit: 100},
			"b": {CPUTotal: 2, MemUsed: 20, MemLimit: 200},
			"c": {CPUTotal: 3, MemUsed: 30, MemLimit: 300},
		},
		df: &dfSample{LayersSize: 1000, ContainersRW: 100, Volumes: 200},
	}
	exporter := &dockerExporter{api: api, project: "jamshid"}
	exporter.collect(context.Background())
	families := collect(t, exporter)

	memory, ok := families["jamshid_container_memory_used_bytes"]
	if !ok {
		t.Fatal("container memory metric missing")
	}
	if len(memory.GetMetric()) != 1 {
		t.Fatalf("want only the jamshid project container, got %d series", len(memory.GetMetric()))
	}
	labels := labelsOf(memory.GetMetric()[0])
	if labels["project"] != "jamshid" || labels["service"] != "backend" || labels["container"] != "jamshid-backend-1" {
		t.Fatalf("unexpected labels %v", labels)
	}
	if memory.GetMetric()[0].GetGauge().GetValue() != 10 {
		t.Fatalf("wrong container exported: memory %v", memory.GetMetric()[0].GetGauge().GetValue())
	}

	// Host-wide disk usage keeps its scope dimension and counts image layers
	// once (LayersSize), not the sum of per-image sizes.
	disk, ok := families["jamshid_docker_disk_usage_bytes"]
	if !ok {
		t.Fatal("disk usage metric missing")
	}
	scopes := map[string]float64{}
	for _, metric := range disk.GetMetric() {
		scopes[labelsOf(metric)["scope"]] = metric.GetGauge().GetValue()
	}
	if scopes["image_layers_unique"] != 1000 {
		t.Fatalf("image_layers_unique = %v, want LayersSize 1000", scopes["image_layers_unique"])
	}
	if scopes["containers_rw"] != 100 || scopes["volumes"] != 200 {
		t.Fatalf("disk scopes wrong: %v", scopes)
	}
}

// TestProjectFilterDisabled: JAMSHID_DOCKER_PROJECT=all exports every
// container, preserving each one's own project label.
func TestProjectFilterDisabled(t *testing.T) {
	api := &fakeAPI{
		containers: []containerInfo{
			{ID: "a", Name: "a", Service: "backend", Project: "jamshid", State: "running"},
			{ID: "b", Name: "b", Service: "backend", Project: "other-project", State: "running"},
		},
		statsByID: map[string]*statsSample{
			"a": {MemUsed: 1},
			"b": {MemUsed: 2},
		},
		df: &dfSample{},
	}
	exporter := &dockerExporter{api: api, project: "all"}
	exporter.collect(context.Background())
	families := collect(t, exporter)
	memory := families["jamshid_container_memory_used_bytes"]
	if len(memory.GetMetric()) != 2 {
		t.Fatalf("want both projects exported, got %d series", len(memory.GetMetric()))
	}
	projects := map[string]bool{}
	for _, metric := range memory.GetMetric() {
		projects[labelsOf(metric)["project"]] = true
	}
	if !projects["jamshid"] || !projects["other-project"] {
		t.Fatalf("project labels missing: %v", projects)
	}
}

// TestStoppedContainerStatsFailureIsVisible: a stopped container whose stats
// request fails must not fail the poll, but the failure has to be exported
// (review finding R6: partial collection failures were invisible).
func TestRunningContainerStatsFailureIsVisible(t *testing.T) {
	api := &fakeAPI{
		containers: []containerInfo{
			{ID: "run", Name: "jamshid-backend-1", Service: "backend", Project: "jamshid", State: "running"},
			{ID: "stop", Name: "jamshid-worker-1", Service: "worker", Project: "jamshid", State: "running"},
		},
		statsByID: map[string]*statsSample{
			"run": {MemUsed: 5},
		},
		statsErr: map[string]error{"stop": errors.New("container is not running")},
		df:       &dfSample{},
	}
	exporter := &dockerExporter{api: api, project: "jamshid"}
	exporter.collect(context.Background())
	families := collect(t, exporter)

	if got := gauge(t, families, "jamshid_docker_exporter_up"); got != 1 {
		t.Fatalf("poll must stay healthy on a per-container stats failure, up=%v", got)
	}
	if got := gauge(t, families, "jamshid_docker_stats_failed"); got != 1 {
		t.Fatalf("stats_failed = %v, want 1", got)
	}
	errorsByContainer := map[string]float64{}
	for _, metric := range families["jamshid_docker_stats_errors_total"].GetMetric() {
		errorsByContainer[labelsOf(metric)["container"]] = metric.GetCounter().GetValue()
	}
	if errorsByContainer["jamshid-worker-1"] != 1 {
		t.Fatalf("stats_errors_total = %v, want 1 for jamshid-worker-1", errorsByContainer)
	}
	// The failed container contributes no resource series; the running one does.
	if len(families["jamshid_container_memory_used_bytes"].GetMetric()) != 1 {
		t.Fatalf("only the running container must export resources")
	}

	// A second failing poll accumulates the counter instead of resetting it.
	exporter.collect(context.Background())
	families = collect(t, exporter)
	for _, metric := range families["jamshid_docker_stats_errors_total"].GetMetric() {
		if labelsOf(metric)["container"] == "jamshid-worker-1" && metric.GetCounter().GetValue() != 2 {
			t.Fatalf("stats_errors_total must accumulate, got %v", metric.GetCounter().GetValue())
		}
	}
}

// TestSocketFailureFlipsHealth: when the Docker API is unreachable the
// exporter keeps serving /metrics but reports up=0.
func TestSocketFailureFlipsHealth(t *testing.T) {
	api := &fakeAPI{listErr: errors.New("cannot connect")}
	exporter := &dockerExporter{api: api, project: "jamshid"}
	exporter.collect(context.Background())
	families := collect(t, exporter)
	if got := gauge(t, families, "jamshid_docker_exporter_up"); got != 0 {
		t.Fatalf("up = %v, want 0 on socket failure", got)
	}
}

func TestStoppedContainerReportsStateWithoutInventingUsage(t *testing.T) {
	api := &fakeAPI{containers: []containerInfo{{ID: "s", Name: "stopped", Project: "jamshid", Service: "worker", State: "exited"}}, df: &dfSample{}}
	exporter := &dockerExporter{api: api, project: "jamshid"}
	exporter.collect(context.Background())
	families := collect(t, exporter)
	if got := gauge(t, families, "jamshid_docker_stats_failed"); got != 0 {
		t.Fatalf("stopped container is not a sampling failure: %v", got)
	}
	if _, exists := families["jamshid_container_memory_used_bytes"]; exists {
		t.Fatal("stopped container must not export fabricated resource samples")
	}
	if got := labelsOf(families["jamshid_container_info"].Metric[0])["state"]; got != "exited" {
		t.Fatalf("missing observed stopped state: %s", got)
	}
}
