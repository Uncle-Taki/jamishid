package http

import (
	"context"
	"errors"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/scrapers"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/aggregation"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/gin-gonic/gin"
)

type AggregationHandler struct {
	jobs            crawljobs.Repository
	catalog         *scrapers.CategoryCatalog
	providerCatalog *scrapers.ProviderCatalog
	service         ports.AggregationUseCase
}

func NewAggregationHandler(service ports.AggregationUseCase) *AggregationHandler {
	return &AggregationHandler{
		service:         service,
		catalog:         scrapers.NewCategoryCatalog(scrapers.NewSharedClient()),
		providerCatalog: scrapers.DefaultProviderCatalog(),
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

	req.Query = strings.TrimSpace(req.Query)
	if len([]rune(req.Keywords)) > 200 {
		c.JSON(400, CrawlResponse{Error: "Search keywords are too long"})
		return
	}
	if req.Query == "" || len([]rune(req.Query)) > 200 || (req.Type != "" && req.Type != vehicle.VehicleTypeCar && req.Type != vehicle.VehicleTypeMotorcycle) || (req.Platform != nil && *req.Platform != vehicle.PlatformDivar && *req.Platform != vehicle.PlatformBama && *req.Platform != vehicle.PlatformSheypoor) || (req.DateCutoffHours != nil && (*req.DateCutoffHours <= 0 || *req.DateCutoffHours > 720)) {
		c.JSON(http.StatusBadRequest, CrawlResponse{Error: "Invalid search criteria"})
		return
	}
	if req.Weights != nil {
		w := req.Weights
		values := []float64{w.PriceWeight, w.ConditionWeight, w.MileageWeight, w.YearWeight}
		sum := 0.0
		for _, v := range values {
			if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
				c.JSON(http.StatusBadRequest, CrawlResponse{Error: "Invalid score weights"})
				return
			}
			sum += v
		}
		if sum <= 0 {
			c.JSON(http.StatusBadRequest, CrawlResponse{Error: "Score weights must have a positive total"})
			return
		}
	}
	if req.BamaCategory != "" && !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,100}$`).MatchString(req.BamaCategory) {
		c.JSON(400, CrawlResponse{Error: "Invalid category"})
		return
	}
	if req.CitySlug != "" && !regexp.MustCompile(`^[a-z0-9-]{1,80}$`).MatchString(req.CitySlug) {
		c.JSON(400, CrawlResponse{Error: "Invalid city"})
		return
	}
	if len(req.Cursors) > 3 {
		c.JSON(400, CrawlResponse{Error: "Invalid cursors"})
		return
	}
	for platform, token := range req.Cursors {
		if (platform != vehicle.PlatformDivar && platform != vehicle.PlatformBama && platform != vehicle.PlatformSheypoor) || len(token) > 32768 {
			c.JSON(400, CrawlResponse{Error: "Invalid cursor"})
			return
		}
	}
	criteria := vehicle.SearchCriteria{
		Keywords:     req.Keywords,
		BamaCategory: req.BamaCategory, Cursors: req.Cursors,
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

	var result vehicle.CrawlResult
	var err error
	if reporter, ok := h.service.(interface {
		AggregateWithSources(context.Context, vehicle.SearchCriteria) (vehicle.CrawlResult, error)
	}); ok {
		result, err = reporter.AggregateWithSources(c.Request.Context(), criteria)
	} else {
		result.Listings, err = h.service.AggregateListings(c.Request.Context(), criteria)
	}
	if result.Listings == nil {
		result.Listings = []vehicle.ScoredListing{}
	}
	response := CrawlResponse{Success: err == nil, Data: result.Listings, Sources: result.Sources, Cached: result.Cached}
	if err != nil {
		if !errors.Is(err, aggregation.ErrSourcesUnavailable) || !ports.DiagnosticRecorded(c.Request.Context()) {
			_ = c.Error(err)
		}
		response.Error = "Failed to fetch marketplace listings"
		status := http.StatusInternalServerError
		if errors.Is(err, aggregation.ErrSourcesUnavailable) {
			status = http.StatusBadGateway
			response.Error = err.Error()
		}
		c.JSON(status, response)
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *AggregationHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *AggregationHandler) Listing(c *gin.Context) {
	platform := vehicle.Platform(c.Query("platform"))
	if platform != vehicle.PlatformDivar && platform != vehicle.PlatformBama && platform != vehicle.PlatformSheypoor {
		c.JSON(400, gin.H{"error": "Invalid source"})
		return
	}
	u := c.Query("url")
	if len(u) > 2048 {
		c.JSON(400, gin.H{"error": "Invalid listing URL"})
		return
	}
	listing, err := scrapers.FetchListingDetails(c.Request.Context(), platform, u)
	if err != nil {
		_ = c.Error(err)
		c.JSON(502, gin.H{"error": "Listing details are unavailable"})
		return
	}
	c.JSON(200, gin.H{"data": listing})
}

func (h *AggregationHandler) Categories(c *gin.Context) {
	kind := vehicle.VehicleType(c.Query("type"))
	parent := c.Query("parent")
	if source := c.Query("source"); source != "" {
		platform := vehicle.Platform(source)
		if (platform != vehicle.PlatformBama && platform != vehicle.PlatformDivar && platform != vehicle.PlatformSheypoor) || (kind != vehicle.VehicleTypeCar && kind != vehicle.VehicleTypeMotorcycle) || len(parent) > 200 || (parent != "" && !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*(/[a-zA-Z0-9][a-zA-Z0-9.-]*){0,4}$`).MatchString(parent)) {
			c.JSON(400, gin.H{"error": "Invalid source category request"})
			return
		}
		rows, err := h.providerCatalog.Categories(c.Request.Context(), platform, kind, parent)
		if err != nil {
			_ = c.Error(err)
			c.JSON(502, gin.H{"error": "Source catalog is unavailable", "detail": err.Error()})
			return
		}
		c.JSON(200, gin.H{"data": rows, "source": source})
		return
	}
	if (kind != vehicle.VehicleTypeCar && kind != vehicle.VehicleTypeMotorcycle) || (parent != "" && !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,100}$`).MatchString(parent)) {
		c.JSON(400, gin.H{"error": "Invalid category request"})
		return
	}
	rows, err := h.catalog.Categories(c.Request.Context(), kind, parent)
	if err != nil {
		_ = c.Error(err)
		c.JSON(502, gin.H{"error": "Marketplace catalog is unavailable"})
		return
	}
	c.JSON(200, gin.H{"data": rows})
}
