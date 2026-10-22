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
		Weights:         req.Weights,
	}

	listings, err := h.service.AggregateListings(c.Request.Context(), criteria)
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
