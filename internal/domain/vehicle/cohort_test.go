package vehicle

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestUtilityWeightsRemainEffectiveWithoutPeers(t *testing.T) {
	row := NormalizedVehicleListing{ID: "one", Platform: PlatformBama, PriceToman: 100, Year: 1402, MileageKm: 0, BodyStatus: BodyStatusChassisHit}
	mileage := CalculateCohortScores(row, []NormalizedVehicleListing{row}, ScoreWeights{MileageWeight: 100})
	condition := CalculateCohortScores(row, []NormalizedVehicleListing{row}, ScoreWeights{ConditionWeight: 100})
	if mileage.TotalScore != 100 || condition.TotalScore != condition.ConditionScore || mileage.TotalScore <= condition.TotalScore || mileage.Confidence != 0 || condition.Confidence != 0 {
		t.Fatalf("preferences lost: mileage=%+v condition=%+v", mileage, condition)
	}
	if score := CalculateCohortScores(row, nil, ScoreWeights{}); score.TotalScore != 50 {
		t.Fatalf("zero weights should be neutral: %+v", score)
	}
}

func TestCohortSharedCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/cohort_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Base  json.RawMessage `json:"base"`
		Cases []struct {
			Name     string         `json:"name"`
			Target   map[string]any `json:"target"`
			Peer     map[string]any `json:"peer"`
			Count    int            `json:"count"`
			Expected struct {
				Count, Confidence int
				Median, Total     float64
			} `json:"expected"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	makeRow := func(overrides map[string]any) NormalizedVehicleListing {
		var values map[string]any
		json.Unmarshal(corpus.Base, &values)
		for k, v := range overrides {
			values[k] = v
		}
		encoded, _ := json.Marshal(values)
		var row NormalizedVehicleListing
		json.Unmarshal(encoded, &row)
		return row
	}
	for _, fixture := range corpus.Cases {
		t.Run(fixture.Name, func(t *testing.T) {
			target := makeRow(fixture.Target)
			pool := []NormalizedVehicleListing{target}
			for i := 0; i < fixture.Count; i++ {
				row := makeRow(fixture.Peer)
				row.ID = fmt.Sprintf("peer-%d", i)
				row.PriceToman = 100
				pool = append(pool, row)
			}
			score := CalculateCohortScores(target, pool, ScoreWeights{PriceWeight: 100})
			expected := fixture.Expected
			if score.ComparableCount != expected.Count || score.Confidence != expected.Confidence || score.MedianPriceToman != expected.Median || score.TotalScore != expected.Total {
				t.Fatalf("got %+v; expected %+v", score, expected)
			}
			if score.PriceBasis != "asking" || score.ScoringVersion != ScoringVersion {
				t.Fatal("basis/version absent")
			}
		})
	}
}
func TestCohortDuplicateAndOutlierStability(t *testing.T) {
	target := NormalizedVehicleListing{ID: "target", Platform: PlatformBama, BrandModel: "پژو 206", TrimName: "5", VehicleType: VehicleTypeCar, Year: 1402, MileageKm: 20000, BodyStatus: BodyStatusPerfect, CityID: "tehran", PriceToman: 85}
	pool := []NormalizedVehicleListing{target}
	for i := 0; i < 10; i++ {
		peer := target
		peer.ID = fmt.Sprint(i)
		peer.PriceToman = 100
		pool = append(pool, peer)
	}
	baseline := buildDealCohort(target, pool)
	duplicates := buildDealCohort(target, append(pool, pool...))
	if len(duplicates.peers) != 10 || duplicates.confidence != baseline.confidence {
		t.Fatal("duplicates inflated confidence")
	}
	outlier := target
	outlier.ID = "outlier"
	outlier.PriceToman = 1000000
	more := buildDealCohort(target, append(pool, outlier))
	if more.reference != baseline.reference || more.confidence != baseline.confidence {
		t.Fatal("one extreme asking price moved benchmark")
	}
}

func TestCohortEmptyModelOrType(t *testing.T) {
	row := NormalizedVehicleListing{ID: "no-model", BrandModel: "", VehicleType: "", PriceToman: 100}
	score := CalculateCohortScores(row, []NormalizedVehicleListing{row}, ScoreWeights{PriceWeight: 100})
	if score.Confidence != 0 {
		t.Errorf("expected 0 confidence for empty model/type, got %d", score.Confidence)
	}
}

func TestScoringIdentity_AudiAliases(t *testing.T) {
	cases := []struct {
		model, trim         string
		expectedM, expectedT string
	}{
		{"audi", "", "آئودی", ""},
		{"audi a4", "", "آئودی a 4", ""},
		{"آئودی q 5 e tron 40", "", "آئودی q 5 e tron", "40"},
		{"آئودی q 5 e tron 50", "50", "آئودی q 5 e tron", "50"},
	}
	for _, c := range cases {
		m, tr := scoringIdentity(NormalizedVehicleListing{BrandModel: c.model, TrimName: c.trim})
		if m != c.expectedM || tr != c.expectedT {
			t.Errorf("scoringIdentity(%q, %q) = (%q, %q); expected (%q, %q)", c.model, c.trim, m, tr, c.expectedM, c.expectedT)
		}
	}
}

func TestMissingEvidence_AllFields(t *testing.T) {
	row := NormalizedVehicleListing{
		PriceToman:  0,
		BrandModel:  "",
		VehicleType: "",
		Year:        0,
		MileageKm:   -1,
		BodyStatus:  BodyStatusUnknown,
		CityID:      "",
		TrimName:    "",
	}
	missing := missingEvidence(row)
	if len(missing) != 8 {
		t.Errorf("expected 8 missing fields, got %d: %v", len(missing), missing)
	}
	if cov := coverage(row); cov != 0 {
		t.Errorf("expected coverage 0, got %f", cov)
	}
}

func TestCohortFilters_GearboxEngineMotorcycleNationalDispersion(t *testing.T) {
	// 1. Different gearbox should be excluded
	target := NormalizedVehicleListing{
		ID: "car1", Platform: PlatformDivar, BrandModel: "پژو 206", TrimName: "5",
		VehicleType: VehicleTypeCar, Year: 1400, MileageKm: 10000,
		Gearbox: "manual", EngineVolumeCc: 1600, PriceToman: 500, CityID: "tehran",
	}
	diffGearboxPeer := target
	diffGearboxPeer.ID = "car-auto"
	diffGearboxPeer.Gearbox = "automatic"

	cohort1 := buildDealCohort(target, []NormalizedVehicleListing{target, diffGearboxPeer})
	if len(cohort1.peers) != 0 {
		t.Fatalf("expected diff gearbox peer to be filtered out, got %d", len(cohort1.peers))
	}

	// 2. Different engine volume should be excluded
	diffEnginePeer := target
	diffEnginePeer.ID = "car-engine"
	diffEnginePeer.EngineVolumeCc = 2000

	cohort2 := buildDealCohort(target, []NormalizedVehicleListing{target, diffEnginePeer})
	if len(cohort2.peers) != 0 {
		t.Fatalf("expected diff engine peer to be filtered out, got %d", len(cohort2.peers))
	}

	// 3. Motorcycle target with annual 6000 mileage window
	motoTarget := NormalizedVehicleListing{
		ID: "moto1", Platform: PlatformBama, BrandModel: "بنلی 180",
		VehicleType: VehicleTypeMotorcycle, Year: 1401, MileageKm: 5000,
		PriceToman: 150, CityID: "tehran",
	}
	motoPeer := motoTarget
	motoPeer.ID = "moto2"
	motoPeer.MileageKm = 10000
	cohort3 := buildDealCohort(motoTarget, []NormalizedVehicleListing{motoTarget, motoPeer})
	if len(cohort3.peers) != 1 {
		t.Fatalf("expected motorcycle peer within 6000km to be matched, got %d", len(cohort3.peers))
	}

	// 4. Target with empty CityID defaults to national scope
	nationalTarget := target
	nationalTarget.CityID = ""
	nationalPeer := target
	nationalPeer.ID = "peer-nat"
	nationalPeer.CityID = "isfahan"
	cohort4 := buildDealCohort(nationalTarget, []NormalizedVehicleListing{nationalTarget, nationalPeer})
	if cohort4.scope != "national" {
		t.Fatalf("expected scope to be national, got %s", cohort4.scope)
	}

	// 5. High dispersion (>0.2) adds "dispersed-prices" reason
	dispTarget := target
	pool := []NormalizedVehicleListing{dispTarget}
	prices := []float64{100, 150, 300, 600, 900}
	for i, p := range prices {
		peer := target
		peer.ID = fmt.Sprintf("peer-disp-%d", i)
		peer.PriceToman = p
		pool = append(pool, peer)
	}
	cohort5 := buildDealCohort(dispTarget, pool)
	hasDispersed := false
	for _, r := range cohort5.reasons {
		if r == "dispersed-prices" {
			hasDispersed = true
			break
		}
	}
	if !hasDispersed {
		t.Fatalf("expected dispersed-prices in reasons, got %v", cohort5.reasons)
	}
}
