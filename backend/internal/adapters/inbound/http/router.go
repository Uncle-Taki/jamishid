package http

import (
	"github.com/gin-gonic/gin"
)

func SetupRouter(handler *AggregationHandler) *gin.Engine {
	r := gin.Default()

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
