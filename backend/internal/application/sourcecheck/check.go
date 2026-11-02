package sourcecheck

import (
	"context"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"time"
)

type Probe struct {
	Platform       vehicle.Platform `json:"source"`
	Name           string           `json:"probe"`
	Outcome        string           `json:"outcome"`
	Failure        string           `json:"failure,omitempty"`
	Candidates     int              `json:"candidates"`
	Valid          int              `json:"valid"`
	Rejected       int              `json:"rejected"`
	MissingPrice   int              `json:"missingPrice"`
	MissingYear    int              `json:"missingYear"`
	MissingMileage int              `json:"missingMileage"`
	DurationMS     int64            `json:"durationMs"`
}
type Report struct {
	At     time.Time `json:"at"`
	Probes []Probe   `json:"probes"`
	Alerts []string  `json:"alerts"`
}
type Fetch func(context.Context, vehicle.Platform, vehicle.SearchCriteria) (vehicle.ListingPage, error)

// Two distinct searches per source prevent a legitimate empty query from becoming an alarm.
func Run(ctx context.Context, fetch Fetch) Report {
	result := Report{At: time.Now().UTC(), Probes: []Probe{}, Alerts: []string{}}
	for _, platform := range []vehicle.Platform{vehicle.PlatformDivar, vehicle.PlatformBama, vehicle.PlatformSheypoor} {
		failed, degraded := 0, 0
		for _, query := range []struct {
			name, text, category string
			kind                 vehicle.VehicleType
		}{{"car", "پژو 206", "peugeot-206ir", vehicle.VehicleTypeCar}, {"motorcycle", "بنلی TNT 180S", "benelli-tnt180s", vehicle.VehicleTypeMotorcycle}} {
			probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			start := time.Now()
			page, err := fetch(ports.WithCrawlPolicy(probeCtx, ports.CrawlPolicy{Refresh: true, SkipDetails: true}), platform, vehicle.SearchCriteria{Query: query.text, BamaCategory: query.category, CitySlug: "tehran", Type: query.kind})
			cancel()
			probe := Probe{Platform: platform, Name: query.name, Outcome: "ok", DurationMS: time.Since(start).Milliseconds(), Candidates: page.Parser.Candidates, Valid: page.Parser.Valid, Rejected: page.Parser.Rejected, MissingPrice: page.Parser.MissingPrice, MissingYear: page.Parser.MissingYear, MissingMileage: page.Parser.MissingMileage}
			if err != nil {
				probe.Outcome = "failure"
				probe.Failure = ports.FailureKind(err)
				failed++
			} else if len(page.Listings) == 0 {
				probe.Outcome = "empty"
			} else if page.Parser.Valid > 0 && (page.Parser.Rejected*2 >= page.Parser.Candidates || page.Parser.MissingPrice == page.Parser.Valid) {
				probe.Outcome = "degraded"
				degraded++
			}
			result.Probes = append(result.Probes, probe)
		}
		if failed == 2 {
			result.Alerts = append(result.Alerts, string(platform)+": both source probes failed; inspect failure kinds before diagnosing parser drift")
		}
		if degraded == 2 {
			result.Alerts = append(result.Alerts, string(platform)+": both probes degraded; inspect schema and critical-field coverage")
		}
	}
	return result
}
