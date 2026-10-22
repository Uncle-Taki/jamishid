package http

import (
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type CrawlRequest struct {
	Query           string                `json:"query" binding:"required"`
	MinPrice        *float64              `json:"minPrice,omitempty"`
	MaxPrice        *float64              `json:"maxPrice,omitempty"`
	MinYear         *int                  `json:"minYear,omitempty"`
	MaxYear         *int                  `json:"maxYear,omitempty"`
	MinMileage      *float64              `json:"minMileage,omitempty"`
	MaxMileage      *float64              `json:"maxMileage,omitempty"`
	CitySlug        string                `json:"citySlug,omitempty"`
	Platform        *vehicle.Platform     `json:"platform,omitempty"`
	Type            vehicle.VehicleType   `json:"type,omitempty"`
	DateCutoffHours *int                  `json:"dateCutoffHours,omitempty"`
	Weights         *vehicle.ScoreWeights `json:"weights,omitempty"`
}

type CrawlResponse struct {
	Success bool                    `json:"success"`
	Data    []vehicle.ScoredListing `json:"data,omitempty"`
	Error   string                  `json:"error,omitempty"`
}
