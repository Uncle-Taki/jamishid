package scrapers

import (
	"context"
	"fmt"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"math"
	"net/url"
	"strings"
)

// Validate before model/recommendation filtering: zero relevant matches is not parser drift.
func validateCollection(ctx context.Context, platform vehicle.Platform, candidates int, rows []vehicle.NormalizedVehicleListing) ([]vehicle.NormalizedVehicleListing, vehicle.ParserReport, error) {
	report := vehicle.ParserReport{Candidates: candidates}
	valid := []vehicle.NormalizedVehicleListing{}
	host := map[vehicle.Platform]string{vehicle.PlatformDivar: "divar.ir", vehicle.PlatformBama: "bama.ir", vehicle.PlatformSheypoor: "sheypoor.com"}[platform]
	for _, row := range rows {
		u, err := url.Parse(row.URL)
		if strings.TrimSpace(row.ID) == "" || strings.TrimSpace(row.Title) == "" || err != nil || u.Scheme != "https" || strings.TrimPrefix(u.Hostname(), "www.") != host || u.User != nil || math.IsNaN(row.PriceToman) || math.IsInf(row.PriceToman, 0) || row.PriceToman < 0 || math.IsNaN(row.MileageKm) || math.IsInf(row.MileageKm, 0) || row.MileageKm < -1 || row.Year < 0 {
			continue
		}
		if row.PriceToman == 0 {
			report.MissingPrice++
		}
		if row.Year == 0 {
			report.MissingYear++
		}
		if row.MileageKm < 0 {
			report.MissingMileage++
		}
		valid = append(valid, row)
	}
	report.Valid = len(valid)
	report.Rejected = max(0, candidates-report.Valid)
	var err error
	outcome := "ok"
	if candidates == 0 {
		outcome = "empty"
	} else if report.Valid == 0 {
		outcome = "schema_failure"
		err = &ports.SourceError{Kind: "parser", Err: fmt.Errorf("%s collection has %d candidates but no valid vehicle identities", platform, candidates)}
	} else if report.Rejected > 0 {
		outcome = "partial"
	}
	_, finish := observer.Start(ctx, "parser_contract", platform)
	finish(ports.Observation{Outcome: outcome, Error: err, Listings: report.Valid})
	return valid, report, err
}
