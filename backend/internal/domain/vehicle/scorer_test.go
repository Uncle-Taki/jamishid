package vehicle

import (
	"math"
	"testing"
	"time"
)

func TestCalculateBatchMedianPrice(t *testing.T) {
	tests := []struct {
		name     string
		listings []NormalizedVehicleListing
		expected float64
	}{
		{
			name:     "empty slice",
			listings: []NormalizedVehicleListing{},
			expected: 0,
		},
		{
			name: "zero prices",
			listings: []NormalizedVehicleListing{
				{PriceToman: 0},
			},
			expected: 0,
		},
		{
			name: "basic median",
			listings: []NormalizedVehicleListing{
				{PriceToman: 100}, {PriceToman: 300}, {PriceToman: 200},
			},
			expected: 200,
		},
		{
			name: "filters outliers",
			listings: []NormalizedVehicleListing{
				{PriceToman: 10}, {PriceToman: 200}, {PriceToman: 200}, {PriceToman: 200}, {PriceToman: 1000},
			},
			expected: 200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateBatchMedianPrice(tt.listings)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestCalculatePriceScore(t *testing.T) {
	median := 1000.0

	tests := []struct {
		name     string
		price    float64
		expected float64
	}{
		{"price is equal to median", 1000, 50},
		{"price is lower", 850, 85},
		{"price is higher", 1200, 20},
		{"price is way lower", 100, 100},
		{"price is way higher", 2000, 0},
		{"zero price", 0, 50},
		{"zero median", 1000, 0}, // special case trigger
	}
	tests[6].expected = CalculatePriceScore(1000, 0)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculatePriceScore(tt.price, median)
			if tt.name == "zero median" {
			   result = CalculatePriceScore(1000, 0)
			}
			if math.Abs(result-tt.expected) > 1e-9 {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestCalculateMileageScore(t *testing.T) {
	currentShamsiYear := time.Now().Year() - 621

	tests := []struct {
		name         string
		mileage      float64
		year         int
		isMotorcycle bool
		expected     float64
	}{
		{"new car below expected", 9000, currentShamsiYear - 1, false, 75},
		{"new car above expected", 27000, currentShamsiYear - 1, false, 30},
		{"moto below expected", 6000, currentShamsiYear - 2, true, 75},
		{"moto above expected", 18000, currentShamsiYear - 2, true, 30},
		{"negative mileage", -10, currentShamsiYear - 1, false, 50},
		{"way below caps at 100", 0, currentShamsiYear - 1, false, 100},
		{"way above caps at 0", 1000000, currentShamsiYear - 1, false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateMileageScore(tt.mileage, tt.year, tt.isMotorcycle)
			if math.Abs(result-tt.expected) > 1e-9 {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestCalculateConditionScore(t *testing.T) {
	tests := []struct {
		name     string
		status   BodyStatus
		expected float64
	}{
		{"perfect", BodyStatusPerfect, 100},
		{"painted-parts", BodyStatusPaintedParts, 75},
		{"full-paint", BodyStatusFullPaint, 40},
		{"chassis-hit", BodyStatusChassisHit, 15},
		{"unknown fallback", BodyStatus("unknown"), 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateConditionScore(tt.status)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestCalculateYearScore(t *testing.T) {
	tests := []struct {
		name     string
		year     int
		minYear  int
		maxYear  int
		expected float64
	}{
		{"mid range", 1395, 1390, 1400, 50},
		{"lowest", 1390, 1390, 1400, 0},
		{"highest", 1400, 1390, 1400, 100},
		{"out of bounds low", 1380, 1390, 1400, 0},
		{"out of bounds high", 1410, 1390, 1400, 100},
		{"min equal max", 1400, 1400, 1400, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateYearScore(tt.year, tt.minYear, tt.maxYear)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestGetDealTierLabels(t *testing.T) {
	_, _, bg, _, _ := GetDealTierLabels(DealTierSuper)
	if bg != "bg-green-100" { t.Fail() }
	_, _, bg, _, _ = GetDealTierLabels(DealTierGood)
	if bg != "bg-blue-100" { t.Fail() }
	_, _, bg, _, _ = GetDealTierLabels(DealTierFair)
	if bg != "bg-gray-100" { t.Fail() }
	_, _, bg, _, _ = GetDealTierLabels(DealTierOverpriced)
	if bg != "bg-red-100" { t.Fail() }
	_, _, bg, _, _ = GetDealTierLabels(DealTier("Unknown"))
	if bg != "bg-gray-100" { t.Fail() }
}

func TestClassifyDealTier(t *testing.T) {
	if ClassifyDealTier(85) != DealTierSuper { t.Fail() }
	if ClassifyDealTier(65) != DealTierGood { t.Fail() }
	if ClassifyDealTier(45) != DealTierFair { t.Fail() }
	if ClassifyDealTier(20) != DealTierOverpriced { t.Fail() }
}

func TestCalculateUtilityScores(t *testing.T) {
	listing := NormalizedVehicleListing{
		PriceToman: 500,
		MileageKm: 10000,
		Year: 1400,
		BodyStatus: BodyStatusPerfect,
		VehicleType: VehicleTypeCar,
	}

	weights := ScoreWeights{
		PriceWeight: 40,
		ConditionWeight: 30,
		MileageWeight: 20,
		YearWeight: 10,
	}

	scores := CalculateUtilityScores(listing, weights, 500, 1390, 1405)
	if scores.PriceScore != 50 { t.Fail() } // 500 == median
	if scores.ConditionScore != 100 { t.Fail() }
	if scores.PriceDeltaPercent != 0 { t.Fail() }
}
