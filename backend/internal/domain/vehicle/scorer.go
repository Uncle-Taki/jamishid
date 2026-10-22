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

// GetDealTierLabels returns localized labels and UI badge classes for a given tier
func GetDealTierLabels(tier DealTier) (labelFa, labelEn, bg, text, border string) {
	switch tier {
	case DealTierSuper:
		return "فرصت طلایی خرید", "Super Deal 🏆", "bg-emerald-500/20", "text-emerald-300", "border-emerald-500/40"
	case DealTierGood:
		return "ارزش خرید بالا", "Good Deal ✨", "bg-blue-500/20", "text-blue-300", "border-blue-500/40"
	case DealTierFair:
		return "قیمت منصفانه بازار", "Fair Market ⚖️", "bg-slate-800", "text-slate-300", "border-slate-700"
	case DealTierOverpriced:
		return "ارزش خرید پایین / گران", "Overpriced / Risky ⚠️", "bg-rose-500/20", "text-rose-400", "border-rose-500/40"
	default:
		return "نامشخص", "Unknown", "bg-gray-100", "text-gray-800", "border-gray-200"
	}
}

// ClassifyDealTier determines the deal tier based on exact constants
func ClassifyDealTier(totalScore float64) DealTier {
	if totalScore >= 85 {
		return DealTierSuper
	} else if totalScore >= 70 {
		return DealTierGood
	} else if totalScore >= 50 {
		return DealTierFair
	} else {
		return DealTierOverpriced
	}
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

	tier := ClassifyDealTier(totalScore)
	labelFa, labelEn, bg, text, border := GetDealTierLabels(tier)

	var priceDelta float64
	if marketMedian > 0 {
		priceDelta = ((listing.PriceToman - marketMedian) / marketMedian) * 100.0
	}

	return ListingScoreBreakdown{
		PriceScore:        math.Round(priceScore),
		MileageScore:      math.Round(mileageScore),
		YearScore:         math.Round(yearScore),
		ConditionScore:    math.Round(conditionScore),
		TotalScore:        math.Round(totalScore),
		DealTier:          tier,
		DealLabelFa:       labelFa,
		DealLabelEn:       labelEn,
		BadgeBg:           bg,
		BadgeText:         text,
		BadgeBorder:       border,
		MedianPriceToman:  marketMedian,
		PriceDeltaPercent: math.Round(priceDelta*10) / 10,
	}
}
