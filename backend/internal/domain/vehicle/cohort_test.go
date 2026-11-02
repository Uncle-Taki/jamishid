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
