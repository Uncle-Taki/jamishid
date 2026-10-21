package vehicle

import (
	"math"
	"sort"
	"time"
)

// CalculateBatchMedianPrice computes the median price after filtering outliers
func CalculateBatchMedianPrice(listings []NormalizedVehicleListing) float64 {
	if len(listings) == 0 {
		return 0
	}

	var prices []float64
	var sum float64
	for _, listing := range listings {
		if listing.PriceToman > 0 {
			prices = append(prices, listing.PriceToman)
			sum += listing.PriceToman
		}
	}

	if len(prices) == 0 {
		return 0
	}

	mean := sum / float64(len(prices))

	// Filter inliers
	var inliers []float64
	for _, p := range prices {
		if p >= 0.5*mean && p <= 1.5*mean {
			inliers = append(inliers, p)
		}
	}

	// Use original if no inliers
	if len(inliers) == 0 {
		inliers = prices
	}

	sort.Float64s(inliers)

	n := len(inliers)
	if n%2 == 0 {
		return (inliers[n/2-1] + inliers[n/2]) / 2.0
	}
	return inliers[n/2]
}

// CalculatePriceScore computes an asymmetric piecewise score
func CalculatePriceScore(price, median float64) float64 {
	if price <= 0 || median <= 0 {
		return 50 // Default
	}

	var score float64
	if price <= median {
		// Below median: 50 + ((median - price) / median) * (35.0 / 0.15)
		score = 50 + ((median-price)/median)*(35.0/0.15)
	} else {
		// Above median: 50 - ((price - median) / median) * (30.0 / 0.20)
		score = 50 - ((price-median)/median)*(30.0/0.20)
	}

	if score > 100 {
		return 100
	}
	if score < 0 {
		return 0
	}
	return score
}

// CalculateMileageScore computes score based on mileage and Shamsi year
func CalculateMileageScore(mileage float64, year int, isMotorcycle bool) float64 {
	if mileage < 0 {
		return 50
	}

	// Dynamically compute current Shamsi year
	currentShamsiYear := time.Now().Year() - 621

	ageYears := currentShamsiYear - year
	if ageYears < 1 {
		ageYears = 1 // max(1, ...)
	}

	expectedAnnual := 18000.0
	if isMotorcycle {
		expectedAnnual = 6000.0
	}

	expected := float64(ageYears) * expectedAnnual

	var score float64
	if mileage <= expected {
		// 100 - (mileage / expected) * 50
		score = 100 - (mileage/expected)*50
	} else {
		// 50 - ((mileage - expected) / expected) * 40
		score = 50 - ((mileage-expected)/expected)*40
	}

	if score > 100 {
		return 100
	}
	if score < 0 {
		return 0
	}
	return score
}

// CalculateConditionScore assigns score based on exact BodyStatus constants
func CalculateConditionScore(status BodyStatus) float64 {
	switch status {
	case BodyStatusPerfect:
		return 100
	case BodyStatusPaintedParts:
		return 75
	case BodyStatusFullPaint:
		return 40
	case BodyStatusChassisHit:
		return 15
	default:
		return 50
	}
}

// CalculateYearScore assigns a normalized score between minYear and maxYear
func CalculateYearScore(year, minYear, maxYear int) float64 {
	if minYear >= maxYear {
		return 100
	}

	if year < minYear {
		year = minYear
	}
	if year > maxYear {
		year = maxYear
	}

	return (float64(year-minYear) / float64(maxYear-minYear)) * 100
}

// CalculateUtilityScores calculates all scores and the weighted total
func CalculateUtilityScores(listing NormalizedVehicleListing, weights ScoreWeights, marketMedian float64, minYear, maxYear int) ListingScoreBreakdown {
	priceScore := CalculatePriceScore(listing.PriceToman, marketMedian)
	mileageScore := CalculateMileageScore(listing.MileageKm, listing.Year, listing.VehicleType == VehicleTypeMotorcycle)
	conditionScore := CalculateConditionScore(listing.BodyStatus)
	yearScore := CalculateYearScore(listing.Year, minYear, maxYear)

	totalScore := (priceScore * weights.PriceWeight) +
		(mileageScore * weights.MileageWeight) +
		(yearScore * weights.YearWeight) +
		(conditionScore * weights.ConditionWeight)

	// Normalize if weights don't sum to 100 (or 1)
	sumWeights := weights.PriceWeight + weights.MileageWeight + weights.YearWeight + weights.ConditionWeight
	if sumWeights > 0 {
		totalScore = totalScore / sumWeights
	}

	return ListingScoreBreakdown{
		PriceScore:     math.Round(priceScore*100) / 100,
		MileageScore:   math.Round(mileageScore*100) / 100,
		YearScore:      math.Round(yearScore*100) / 100,
		ConditionScore: math.Round(conditionScore*100) / 100,
		TotalScore:     math.Round(totalScore*100) / 100,
	}
}

// ClassifyDealTier determines the deal tier based on exact constants
func ClassifyDealTier(totalScore float64) DealTier {
	if totalScore >= 80 {
		return DealTierSuper
	} else if totalScore >= 60 {
		return DealTierGood
	} else if totalScore >= 40 {
		return DealTierFair
	} else {
		return DealTierOverpriced
	}
}
