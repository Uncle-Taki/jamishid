package vehicle

import "time"

type SearchCriteria struct {
	Keywords        string              `json:"keywords,omitempty"`
	BamaCategory    string              `json:"bamaCategory,omitempty"`
	Cursors         map[Platform]string `json:"cursors,omitempty"`
	Query           string              `json:"query"`
	MinPrice        *float64            `json:"minPrice,omitempty"`
	MaxPrice        *float64            `json:"maxPrice,omitempty"`
	MinYear         *int                `json:"minYear,omitempty"`
	MaxYear         *int                `json:"maxYear,omitempty"`
	MinMileage      *float64            `json:"minMileage,omitempty"`
	MaxMileage      *float64            `json:"maxMileage,omitempty"`
	CitySlug        string              `json:"citySlug,omitempty"`
	Platform        *Platform           `json:"platform,omitempty"`
	Type            VehicleType         `json:"type,omitempty"`
	DateCutoffHours *int                `json:"dateCutoffHours,omitempty"`
	Weights         *ScoreWeights       `json:"weights,omitempty"`
}

type DateCutoff struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}
