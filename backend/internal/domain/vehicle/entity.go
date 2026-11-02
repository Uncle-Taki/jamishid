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
	BodyStatusUnknown      BodyStatus = "unknown"
	BodyStatusPerfect      BodyStatus = "perfect"
	BodyStatusPaintedParts BodyStatus = "painted-parts"
	BodyStatusFullPaint    BodyStatus = "full-paint"
	BodyStatusChassisHit   BodyStatus = "chassis-hit"
)

type DealTier string

const (
	DealTierInsufficient DealTier = "insufficient-data"
	DealTierSuper        DealTier = "super-deal"
	DealTierGood         DealTier = "good-deal"
	DealTierFair         DealTier = "fair-market"
	DealTierOverpriced   DealTier = "overpriced"
)

type FactProvenance struct {
	ObservationID int64    `json:"observationId"`
	Source        Platform `json:"source"`
	URL           string   `json:"url"`
	ObservedAt    string   `json:"observedAt"`
	Kind          string   `json:"kind"`
}
type ListingLifecycle struct {
	Availability string  `json:"availability"`
	FirstSeen    string  `json:"firstSeen"`
	LastSeen     string  `json:"lastSeen"`
	LastVerified *string `json:"lastVerified"`
	LastChecked  *string `json:"lastChecked"`
	CheckError   string  `json:"checkError"`
}
type NormalizedVehicleListing struct {
	Lifecycle       *ListingLifecycle         `json:"lifecycle,omitempty"`
	Provenance      map[string]FactProvenance `json:"provenance,omitempty"`
	Attributes      []SourceAttribute         `json:"attributes,omitempty"`
	DetailsStatus   string                    `json:"detailsStatus,omitempty"`
	EngineVolumeCc  float64                   `json:"engineVolumeCc,omitempty"`
	TechnicalStatus string                    `json:"technicalStatus,omitempty"`
	DocumentStatus  string                    `json:"documentStatus,omitempty"`
	TrimName        string                    `json:"trimName,omitempty"`
	BrandModel      string                    `json:"brandModel,omitempty"`
	PublishedAt     string                    `json:"publishedAt,omitempty"`
	ImageURL        string                    `json:"imageUrl,omitempty"`
	ID              string                    `json:"id"`
	Platform        Platform                  `json:"platform"`
	Title           string                    `json:"title"`
	PriceToman      float64                   `json:"priceToman"`
	MileageKm       float64                   `json:"mileageKm"`
	Year            int                       `json:"year"`
	BodyStatus      BodyStatus                `json:"bodyStatus"`
	URL             string                    `json:"url"`
	ScrapedAt       string                    `json:"scrapedAt"` // ISO 8601 string
	CityID          string                    `json:"cityId,omitempty"`
	NeighborhoodFa  string                    `json:"neighborhoodFa,omitempty"`
	ColorFa         string                    `json:"colorFa,omitempty"`
	Gearbox         string                    `json:"gearbox,omitempty"`
	FuelType        string                    `json:"fuelType,omitempty"`
	VehicleType     VehicleType               `json:"vehicleType,omitempty"`
}

// SourceAttribute keeps the original labelled value alongside its canonical key.
type SourceAttribute struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
}

type ScoreWeights struct {
	PriceWeight     float64 `json:"price"`
	ConditionWeight float64 `json:"condition"`
	MileageWeight   float64 `json:"mileage"`
	YearWeight      float64 `json:"year"`
}

type ListingScoreBreakdown struct {
	Confidence        int      `json:"confidence"`
	ConfidenceLevel   string   `json:"confidenceLevel"`
	ComparableCount   int      `json:"comparableCount"`
	CohortScope       string   `json:"cohortScope"`
	MissingFields     []string `json:"missingFields"`
	ConfidenceReasons []string `json:"confidenceReasons"`
	PriceBasis        string   `json:"priceBasis"`
	ScoringVersion    string   `json:"scoringVersion"`
	PriceScore        float64  `json:"priceScore"`
	ConditionScore    float64  `json:"conditionScore"`
	MileageScore      float64  `json:"mileageScore"`
	YearScore         float64  `json:"yearScore"`
	TotalScore        float64  `json:"totalScore"`
	DealTier          DealTier `json:"dealTier"`
	DealLabelFa       string   `json:"dealLabelFa"`
	DealLabelEn       string   `json:"dealLabelEn"`
	BadgeBg           string   `json:"badgeBg"`
	BadgeText         string   `json:"badgeText"`
	BadgeBorder       string   `json:"badgeBorder"`
	MedianPriceToman  float64  `json:"medianPriceToman"`
	PriceDeltaPercent float64  `json:"priceDeltaPercent"`
}

type ScoredListing struct {
	NormalizedVehicleListing
	Score ListingScoreBreakdown `json:"score"`
}

// SourceStatus describes an actual upstream attempt, including partial failures.
type SourceStatus struct {
	FailureKind string   `json:"failureKind,omitempty"`
	NextCursor  string   `json:"nextCursor,omitempty"`
	Page        int      `json:"page,omitempty"`
	Platform    Platform `json:"platform"`
	Status      string   `json:"status"`
	Count       int      `json:"count"`
	Error       string   `json:"error,omitempty"`
}

type CrawlResult struct {
	Listings []ScoredListing
	Sources  []SourceStatus
	Cached   bool
}

type Category struct {
	ID       string      `json:"id"`
	Label    string      `json:"label"`
	ParentID string      `json:"parentId,omitempty"`
	Type     VehicleType `json:"type"`
}
type ParserReport struct {
	Candidates     int `json:"candidates"`
	Valid          int `json:"valid"`
	Rejected       int `json:"rejected"`
	MissingPrice   int `json:"missingPrice"`
	MissingYear    int `json:"missingYear"`
	MissingMileage int `json:"missingMileage"`
}
type ListingPage struct {
	Parser          ParserReport
	CategoryMatched bool
	Listings        []NormalizedVehicleListing
	NextCursor      string
	Page            int
}
