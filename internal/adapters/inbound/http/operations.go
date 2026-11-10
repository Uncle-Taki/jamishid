package http

import (
	"context"
	"crypto/subtle"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/postgres"
	"github.com/gin-gonic/gin"
	"strings"
	"time"
)

type OperationsStore interface {
	Coverage(ctx context.Context) ([]postgres.CoverageStatus, error)
	Proxies(ctx context.Context) ([]postgres.EgressProxy, error)
	PutProxy(ctx context.Context, proxyID, endpoint string, enabled bool) (string, error)
	DeleteProxy(ctx context.Context, proxyID string) error
}

// Administrative endpoints are disabled unless the operator supplies a token.
func ConfigureOperations(router *gin.Engine, store OperationsStore, token string) {
	router.GET("/api/v1/coverage", func(c *gin.Context) {
		if store == nil {
			c.JSON(503, gin.H{"error": "Coverage requires PostgreSQL"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		result, err := store.Coverage(ctx)
		if err != nil {
			jobError(c, err)
			return
		}
		c.JSON(200, gin.H{"targetHours": 48, "sources": result})
	})
	if token == "" {
		return
	}
	admin := router.Group("/api/v1/admin", func(c *gin.Context) {
		if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte("Bearer "+token)) != 1 {
			c.AbortWithStatus(401)
			return
		}
		c.Next()
	})
	admin.GET("/proxies", func(c *gin.Context) {
		rows, err := store.Proxies(c.Request.Context())
		if err != nil {
			jobError(c, err)
			return
		}
		c.JSON(200, gin.H{"data": rows})
	})
	admin.PUT("/proxies/:id", func(c *gin.Context) {
		var request struct {
			Endpoint string `json:"endpoint"`
			Enabled  *bool  `json:"enabled"`
		}
		if c.ShouldBindJSON(&request) != nil || len(request.Endpoint) > 2048 || postgres.ValidateProxy(request.Endpoint) != nil {
			c.JSON(400, gin.H{"error": "Provide a valid proxy endpoint with scheme, host and port"})
			return
		}
		proxyID := c.Param("id")
		if len(proxyID) > 80 || strings.Trim(proxyID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
			c.AbortWithStatus(400)
			return
		}
		enabled := true
		if request.Enabled != nil {
			enabled = *request.Enabled
		}
		result, err := store.PutProxy(c.Request.Context(), proxyID, request.Endpoint, enabled)
		if err != nil {
			jobError(c, err)
			return
		}
		c.JSON(200, gin.H{"id": result})
	})
	admin.DELETE("/proxies/:id", func(c *gin.Context) {
		if err := store.DeleteProxy(c.Request.Context(), c.Param("id")); err != nil {
			jobError(c, err)
			return
		}
		c.Status(204)
	})
}
