package vehicle

type Platform string

const (
	PlatformDivar    Platform = "divar"
	PlatformBama     Platform = "bama"
	PlatformSheypoor Platform = "sheypoor"
)

type VehicleType string

const (
	VehicleTypeCar        VehicleType = "car"
	VehicleTypeMotorcycle VehicleType = "motorcycle"
	VehicleTypeTruck      VehicleType = "truck"
)

type BodyStatus string

const (
	BodyStatusPerfect      BodyStatus = "perfect"
	BodyStatusPaintedParts BodyStatus = "painted-parts"
	BodyStatusFullPaint    BodyStatus = "full-paint"
	BodyStatusChassisHit   BodyStatus = "chassis-hit"
)

type DealTier string

const (
	DealTierSuper      DealTier = "super-deal"
	DealTierGood       DealTier = "good-deal"
	DealTierFair       DealTier = "fair-market"
	DealTierOverpriced DealTier = "overpriced"
)

type NormalizedVehicleListing struct {
	ID             string      `json:"id"`
	Platform       Platform    `json:"platform"`
	Title          string      `json:"title"`
	PriceToman     float64     `json:"priceToman"`
	MileageKm      float64     `json:"mileageKm"`
	Year           int         `json:"year"`
	BodyStatus     BodyStatus  `json:"bodyStatus"`
	URL            string      `json:"url"`
	ScrapedAt      string      `json:"scrapedAt"` // ISO 8601 string
	CityID         string      `json:"cityId,omitempty"`
	NeighborhoodFa string      `json:"neighborhoodFa,omitempty"`
	ColorFa        string      `json:"colorFa,omitempty"`
	Gearbox        string      `json:"gearbox,omitempty"`
	FuelType       string      `json:"fuelType,omitempty"`
	VehicleType    VehicleType `json:"vehicleType,omitempty"`
}

type ScoreWeights struct {
	PriceWeight     float64 `json:"price"`
	ConditionWeight float64 `json:"condition"`
	MileageWeight   float64 `json:"mileage"`
	YearWeight      float64 `json:"year"`
}

type ListingScoreBreakdown struct {
	PriceScore     float64 `json:"priceScore"`
	MileageScore   float64 `json:"mileageScore"`
	YearScore      float64 `json:"yearScore"`
	ConditionScore float64 `json:"conditionScore"`
	TotalScore     float64 `json:"totalScore"`
}

type ScoredListing struct {
	NormalizedVehicleListing
	Score    ListingScoreBreakdown `json:"score"`
	DealTier DealTier              `json:"dealTier"`
}
