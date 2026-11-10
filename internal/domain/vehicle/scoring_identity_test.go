package vehicle

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPublicQ5PairUsesSameCohortAfterCityRepair(t *testing.T) {
	raw, err := os.ReadFile("testdata/q5_pair.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Listings []NormalizedVehicleListing `json:"listings"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	pair := fixture.Listings
	model, trim := scoringIdentity(pair[0])
	otherModel, otherTrim := scoringIdentity(pair[1])
	if model != otherModel || trim != otherTrim {
		t.Fatal("source representations split the same model/trim")
	}
	pool := append([]NormalizedVehicleListing{}, pair...)
	for _, id := range []string{"peer-a", "peer-b", "peer-c", "peer-d", "peer-e"} {
		peer := pair[0]
		peer.ID = id
		peer.PriceToman = 18000000000
		pool = append(pool, peer)
	}
	weights := ScoreWeights{PriceWeight: 40, ConditionWeight: 30, MileageWeight: 20, YearWeight: 10}
	a, b := CalculateCohortScores(pair[0], pool, weights), CalculateCohortScores(pair[1], pool, weights)
	if a.TotalScore != b.TotalScore || a.PriceScore != b.PriceScore || a.MedianPriceToman != b.MedianPriceToman || a.Confidence != b.Confidence || a.ComparableCount != 6 || b.ComparableCount != 6 {
		t.Fatalf("inconsistent Q5 scores: %+v %+v", a, b)
	}
	for _, variant := range []struct{ model, trim string }{{"آئودی Q5", "40"}, {"آئودی کیو 5 ای ترون 50", ""}, {"آئودی کیو 5 ای ترون 40", "50"}} {
		peer := pair[1]
		peer.BrandModel, peer.TrimName = variant.model, variant.trim
		if cohort := buildDealCohort(pair[0], []NormalizedVehicleListing{peer}); len(cohort.peers) != 0 {
			t.Errorf("incompatible variant accepted: %+v", variant)
		}
	}
}
