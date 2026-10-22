package http

import (
	"context"
	"net/http"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/gin-gonic/gin"
)

type AggregationHandler struct {
	service ports.AggregationUseCase
}

func NewAggregationHandler(service ports.AggregationUseCase) *AggregationHandler {
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

	reqType := req.Type
	if reqType == "" {
		reqType = vehicle.VehicleTypeCar
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
		Type:            reqType,
		DateCutoffHours: req.DateCutoffHours,
		Weights:         req.Weights,
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	listings, err := h.service.AggregateListings(ctx, criteria)
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
