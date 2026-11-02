package http

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/gin-gonic/gin"
)

func (h *AggregationHandler) SetJobs(store crawljobs.Repository) { h.jobs = store }
func validPlatform(p vehicle.Platform) bool {
	return p == vehicle.PlatformDivar || p == vehicle.PlatformBama || p == vehicle.PlatformSheypoor
}
func jobError(c *gin.Context, err error) {
	if errors.Is(err, crawljobs.ErrNotFound) {
		c.JSON(404, gin.H{"error": "Crawl job not found"})
	} else {
		c.JSON(503, gin.H{"error": "Stored crawl service unavailable"})
	}
}
func (h *AggregationHandler) CreateJob(c *gin.Context) {
	if h.jobs == nil {
		c.JSON(503, gin.H{"error": "Background crawling requires PostgreSQL"})
		return
	}
	var request struct {
		CrawlRequest
		Refresh bool `json:"refresh"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(400, gin.H{"error": "Invalid crawl request"})
		return
	}
	r := request.CrawlRequest
	r.Query = strings.TrimSpace(r.Query)
	if r.Query == "" || len([]rune(r.Query)) > 200 || len([]rune(r.Keywords)) > 200 || (r.Type != "" && r.Type != vehicle.VehicleTypeCar && r.Type != vehicle.VehicleTypeMotorcycle) || (r.Platform != nil && !validPlatform(*r.Platform)) || (r.CitySlug != "" && !regexp.MustCompile(`^[a-z0-9-]{1,80}$`).MatchString(r.CitySlug)) || (r.BamaCategory != "" && !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,100}$`).MatchString(r.BamaCategory)) {
		c.JSON(400, gin.H{"error": "Invalid search criteria"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	job, err := h.jobs.Submit(ctx, vehicle.SearchCriteria{Query: r.Query, Keywords: r.Keywords, BamaCategory: r.BamaCategory, CitySlug: r.CitySlug, Type: r.Type, Platform: r.Platform}, request.Refresh)
	if err != nil {
		jobError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, job)
}
func (h *AggregationHandler) Job(c *gin.Context) {
	if h.jobs == nil {
		c.JSON(503, gin.H{"error": "Background crawling unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	job, err := h.jobs.Get(ctx, c.Param("id"))
	if err != nil {
		jobError(c, err)
		return
	}
	c.JSON(200, job)
}
func (h *AggregationHandler) JobAction(c *gin.Context) {
	if h.jobs == nil {
		c.JSON(503, gin.H{"error": "Background crawling unavailable"})
		return
	}
	var request struct {
		Action   string           `json:"action"`
		Platform vehicle.Platform `json:"platform"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || (request.Action != "pause" && request.Action != "resume" && request.Action != "retry") || (request.Platform != "" && !validPlatform(request.Platform)) {
		c.JSON(400, gin.H{"error": "Invalid job action"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if err := h.jobs.Action(ctx, c.Param("id"), request.Action, request.Platform); err != nil {
		jobError(c, err)
		return
	}
	job, err := h.jobs.Get(ctx, c.Param("id"))
	if err != nil {
		jobError(c, err)
		return
	}
	c.JSON(200, job)
}
func (h *AggregationHandler) Inventory(c *gin.Context) {
	if h.jobs == nil {
		c.JSON(503, gin.H{"error": "Stored inventory requires PostgreSQL"})
		return
	}
	q := crawljobs.InventoryQuery{Text: c.Query("q"), Type: c.Query("type"), City: c.Query("city"), Platform: c.Query("platform"), Category: c.Query("category"), Body: c.Query("body"), Sort: c.Query("sort"), Limit: 50}
	if len([]rune(q.Text)) > 200 || (q.Type != "" && q.Type != "car" && q.Type != "motorcycle") || (q.Platform != "" && !validPlatform(vehicle.Platform(q.Platform))) {
		c.JSON(400, gin.H{"error": "Invalid inventory filters"})
		return
	}
	for key, target := range map[string]**float64{"minPrice": &q.MinPrice, "maxPrice": &q.MaxPrice, "minMileage": &q.MinMileage, "maxMileage": &q.MaxMileage} {
		if raw := c.Query(key); raw != "" {
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil || value < 0 || math.IsInf(value, 0) || math.IsNaN(value) {
				c.JSON(400, gin.H{"error": "Invalid numeric filter"})
				return
			}
			*target = &value
		}
	}
	for key, target := range map[string]**int{"minYear": &q.MinYear, "maxYear": &q.MaxYear} {
		if raw := c.Query(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1300 || value > 1500 {
				c.JSON(400, gin.H{"error": "Invalid year filter"})
				return
			}
			*target = &value
		}
	}
	for key, target := range map[string]*int{"limit": &q.Limit, "offset": &q.Offset} {
		if raw := c.Query(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 || (key == "limit" && (value < 1 || value > 100)) || (key == "offset" && value > 100000) {
				c.JSON(400, gin.H{"error": "Invalid pagination"})
				return
			}
			*target = value
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	result, err := h.jobs.Search(ctx, q)
	if err != nil {
		jobError(c, err)
		return
	}
	c.JSON(200, result)
}

func (h *AggregationHandler) ListingHistory(c *gin.Context) {
	store, ok := h.jobs.(crawljobs.LifecycleRepository)
	if !ok {
		c.JSON(503, gin.H{"error": "Lifecycle storage unavailable"})
		return
	}
	platform := vehicle.Platform(c.Param("platform"))
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if !validPlatform(platform) || err != nil || offset < 0 || offset > 100000 {
		c.JSON(400, gin.H{"error": "Invalid history request"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	history, err := store.History(ctx, platform, c.Param("listingID"), offset)
	if err != nil {
		jobError(c, err)
		return
	}
	c.JSON(200, history)
}
func (h *AggregationHandler) RecheckListing(c *gin.Context) {
	store, ok := h.jobs.(crawljobs.LifecycleRepository)
	if !ok {
		c.JSON(503, gin.H{"error": "Lifecycle storage unavailable"})
		return
	}
	platform := vehicle.Platform(c.Param("platform"))
	if !validPlatform(platform) {
		c.JSON(400, gin.H{"error": "Invalid platform"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if err := store.Recheck(ctx, platform, c.Param("listingID")); err != nil {
		jobError(c, err)
		return
	}
	c.JSON(202, gin.H{"status": "queued"})
}
