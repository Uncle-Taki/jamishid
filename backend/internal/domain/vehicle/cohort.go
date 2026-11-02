package vehicle

import (
	"math"
	"sort"
)

const ScoringVersion = "utility-v2"

func modelIdentity(text string) string { return NormalizeModelText(text) }
func finitePositive(n float64) bool    { return n > 0 && !math.IsNaN(n) && !math.IsInf(n, 0) }
func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	values = append([]float64(nil), values...)
	sort.Float64s(values)
	n := len(values)
	if n%2 == 0 {
		return (values[n/2-1] + values[n/2]) / 2
	}
	return values[n/2]
}
func missingEvidence(row NormalizedVehicleListing) []string {
	fields := []string{}
	if !finitePositive(row.PriceToman) {
		fields = append(fields, "price")
	}
	if modelIdentity(row.BrandModel) == "" {
		fields = append(fields, "model")
	}
	if row.VehicleType == "" {
		fields = append(fields, "type")
	}
	if ScoringYear(row.Year) <= 0 {
		fields = append(fields, "year")
	}
	if row.MileageKm < 0 || math.IsNaN(row.MileageKm) || math.IsInf(row.MileageKm, 0) {
		fields = append(fields, "mileage")
	}
	if row.BodyStatus == "" || row.BodyStatus == BodyStatusUnknown {
		fields = append(fields, "condition")
	}
	if row.CityID == "" {
		fields = append(fields, "region")
	}
	if modelIdentity(row.TrimName) == "" {
		fields = append(fields, "trim")
	}
	return fields
}
func coverage(row NormalizedVehicleListing) float64 { return float64(8-len(missingEvidence(row))) / 8 }

type dealCohort struct {
	peers            []NormalizedVehicleListing
	reference        float64
	confidence       int
	scope, level     string
	missing, reasons []string
}

func buildDealCohort(target NormalizedVehicleListing, pool []NormalizedVehicleListing) dealCohort {
	result := dealCohort{scope: "none", level: "low", missing: missingEvidence(target), reasons: []string{}}
	seen := map[string]bool{string(target.Platform) + ":" + target.ID: true}
	model := modelIdentity(target.BrandModel)
	candidates := []NormalizedVehicleListing{}
	local := []NormalizedVehicleListing{}
	if model != "" && target.VehicleType != "" {
		for _, peer := range pool {
			key := string(peer.Platform) + ":" + peer.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			if !finitePositive(peer.PriceToman) || peer.VehicleType != target.VehicleType || modelIdentity(peer.BrandModel) != model {
				continue
			}
			if target.TrimName != "" && peer.TrimName != "" && modelIdentity(target.TrimName) != modelIdentity(peer.TrimName) {
				continue
			}
			if target.Gearbox != "" && peer.Gearbox != "" && modelIdentity(target.Gearbox) != modelIdentity(peer.Gearbox) {
				continue
			}
			if target.EngineVolumeCc > 0 && peer.EngineVolumeCc > 0 && math.Abs(target.EngineVolumeCc-peer.EngineVolumeCc) > math.Max(5, target.EngineVolumeCc*0.05) {
				continue
			}
			year, otherYear := ScoringYear(target.Year), ScoringYear(peer.Year)
			if year > 0 && otherYear > 0 && math.Abs(float64(year-otherYear)) > 1 {
				continue
			}
			annual := 18000.0
			if target.VehicleType == VehicleTypeMotorcycle {
				annual = 6000
			}
			if target.MileageKm >= 0 && peer.MileageKm >= 0 && math.Abs(target.MileageKm-peer.MileageKm) > math.Max(annual, target.MileageKm*0.25) {
				continue
			}
			if target.BodyStatus != "" && target.BodyStatus != BodyStatusUnknown && peer.BodyStatus != "" && peer.BodyStatus != BodyStatusUnknown && target.BodyStatus != peer.BodyStatus {
				continue
			}
			candidates = append(candidates, peer)
			if target.CityID != "" && peer.CityID == target.CityID {
				local = append(local, peer)
			}
		}
	}
	result.peers = candidates
	if len(local) >= 5 {
		result.peers = local
	}
	if len(result.peers) > 0 {
		result.scope = "local"
		if target.CityID == "" {
			result.scope = "national"
		}
		for _, p := range result.peers {
			if p.CityID != target.CityID {
				result.scope = "national"
			}
		}
	}
	prices := []float64{}
	peerCoverage := 0.0
	missingTrim := target.TrimName == ""
	incomplete := false
	for _, p := range result.peers {
		prices = append(prices, p.PriceToman)
		peerCoverage += coverage(p)
		missingTrim = missingTrim || p.TrimName == ""
		incomplete = incomplete || len(missingEvidence(p)) > 0
	}
	reference := median(prices)
	deviations := []float64{}
	for _, price := range prices {
		deviations = append(deviations, math.Abs(price-reference))
	}
	dispersion := 0.0
	if reference > 0 {
		dispersion = median(deviations) / reference
	}
	n := len(result.peers)
	if n >= 3 {
		result.reference = reference
	}
	if finitePositive(target.PriceToman) && n >= 3 {
		region := 1.0
		if result.scope == "national" {
			region = 0.75
		}
		result.confidence = int(math.Round(100 * math.Min(1, float64(n)/10) * coverage(target) * (peerCoverage / float64(n)) * region / (1 + 4*dispersion)))
	}
	if missingTrim && result.confidence > 65 {
		result.confidence = 65
	}
	if result.confidence >= 75 {
		result.level = "high"
	} else if result.confidence >= 40 {
		result.level = "medium"
	}
	if n < 5 {
		result.reasons = append(result.reasons, "small-sample")
	}
	if len(result.missing) > 0 {
		result.reasons = append(result.reasons, "missing-fields")
	}
	if incomplete {
		result.reasons = append(result.reasons, "incomplete-peers")
	}
	if result.scope == "national" {
		result.reasons = append(result.reasons, "national-fallback")
	}
	if dispersion > 0.2 {
		result.reasons = append(result.reasons, "dispersed-prices")
	}
	return result
}

// CalculateCohortScores uses independent, compatible asking-price peers from the
// supplied search scope. Confidence is an evidence heuristic, never a probability.
func CalculateCohortScores(listing NormalizedVehicleListing, pool []NormalizedVehicleListing, weights ScoreWeights) ListingScoreBreakdown {
	cohort := buildDealCohort(listing, pool)
	minYear, maxYear := 9999, 0
	for _, row := range append([]NormalizedVehicleListing{listing}, cohort.peers...) {
		year := ScoringYear(row.Year)
		if year > 0 && year < minYear {
			minYear = year
		}
		if year > maxYear {
			maxYear = year
		}
	}
	score := CalculateUtilityScores(listing, weights, cohort.reference, minYear, maxYear)
	sum := weights.PriceWeight + weights.ConditionWeight + weights.MileageWeight + weights.YearWeight
	raw := 50.0
	if sum > 0 {
		raw = (score.PriceScore*weights.PriceWeight + score.ConditionScore*weights.ConditionWeight + score.MileageScore*weights.MileageWeight + score.YearScore*weights.YearWeight) / sum
	}
	score.TotalScore = math.Round(math.Max(0, math.Min(100, raw)))
	score.DealTier = ClassifyDealTier(score.TotalScore)
	if cohort.confidence < 40 {
		score.DealTier = DealTierInsufficient
	}
	score.DealLabelFa, score.DealLabelEn, score.BadgeBg, score.BadgeText, score.BadgeBorder = GetDealTierLabels(score.DealTier)
	if !finitePositive(listing.PriceToman) {
		score.DealLabelFa = "قیمت نامشخص"
		score.DealLabelEn = "Price unavailable"
	}
	score.Confidence = cohort.confidence
	score.ConfidenceLevel = cohort.level
	score.ComparableCount = len(cohort.peers)
	score.CohortScope = cohort.scope
	score.MissingFields = cohort.missing
	score.ConfidenceReasons = cohort.reasons
	score.PriceBasis = "asking"
	score.ScoringVersion = ScoringVersion
	return score
}
