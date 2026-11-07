package http

import (
	"errors"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/gin-gonic/gin"
)

func SetupRouter(handler *AggregationHandler) *gin.Engine {
	r := gin.New()
	// Do not serialize raw panic values, headers or request URLs into logs.
	r.Use(func(c *gin.Context) {
		defer func() {
			if recover() != nil {
				ports.ReportDiagnostic(c.Request.Context(), ports.Diagnostic{Component: "http", Operation: c.Request.Method + " " + c.FullPath(), ErrorClass: "panic"}, errors.New("panic recovered"))
				c.AbortWithStatus(500)
			}
		}()
		c.Next()
	})

	// Distinct 405s keep wrong-method requests visible to route metrics.
	r.HandleMethodNotAllowed = true

	// Report the matched route template to the outer telemetry middleware.
	// This middleware must stay first so OPTIONS aborts in the CORS middleware
	// below still leave the capture populated (unmatched routes stay empty).
	r.Use(func(c *gin.Context) {
		if capture := ports.RouteCaptureFrom(c.Request.Context()); capture != nil {
			capture.Template = c.FullPath()
		}
		c.Next()
		if c.Writer.Status() >= 500 && len(c.Errors) > 0 {
			ports.ReportDiagnostic(c.Request.Context(), ports.Diagnostic{Component: "http", Operation: c.Request.Method + " " + c.FullPath()}, c.Errors.Last().Err)
		}
	})

	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	v1 := r.Group("/api/v1")
	{
		v1.GET("/health", handler.Health)
		v1.POST("/crawl", handler.Crawl)
		v1.POST("/jobs", handler.CreateJob)
		v1.GET("/jobs/:id", handler.Job)
		v1.POST("/jobs/:id/actions", handler.JobAction)
		v1.GET("/inventory", handler.Inventory)
		v1.GET("/inventory/:platform/:listingID/history", handler.ListingHistory)
		v1.POST("/inventory/:platform/:listingID/recheck", handler.RecheckListing)
		v1.GET("/categories", handler.Categories)
		v1.GET("/listing", handler.Listing)
	}

	return r
}
