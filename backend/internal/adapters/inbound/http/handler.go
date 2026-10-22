package http

import (
	"net/http"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/aggregation"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/gin-gonic/gin"
)

type AggregationHandler struct {
	service *aggregation.AggregationService
}

func NewAggregationHandler(service *aggregation.AggregationService) *AggregationHandler {
	return &AggregationHandler{
		service: service,
	}
}

func (h *AggregationHandler) Crawl(c *gin.Context) {
	var req CrawlRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, CrawlResponse{
			Success: false,
			Error:   "Invalid request format: " + err.Error(),
		})
		return
	}

	criteria := vehicle.SearchCriteria{
		Query:           req.Query,
		MinPrice:        req.MinPrice,
		MaxPrice:        req.MaxPrice,
		MinYear:         req.MinYear,
		MaxYear:         req.MaxYear,
		MinMileage:      req.MinMileage,
		MaxMileage:      req.MaxMileage,
		CitySlug:        req.CitySlug,
		Platform:        req.Platform,
		Type:            req.Type,
		DateCutoffHours: req.DateCutoffHours,
	}

	// Update service weights dynamically if provided
	svc := h.service
	if req.Weights != nil {
		// Create a shallow copy or a new service temporarily
		// We could refactor service.AggregateListings to take weights as a param,
		// but since the architecture passes it to NewAggregationService,
		// we'll leave it as is or could inject at request scope.
		// For simplicity, we just inject at request scope here.
		svc = aggregation.NewAggregationService(
			h.service.GetScrapers(),
			h.service.GetCache(),
			*req.Weights,
		)
	}

	listings, err := svc.AggregateListings(c.Request.Context(), criteria)
	if err != nil {
		c.JSON(http.StatusInternalServerError, CrawlResponse{
			Success: false,
			Error:   "Failed to aggregate listings: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, CrawlResponse{
		Success: true,
		Data:    listings,
	})
}

func (h *AggregationHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
