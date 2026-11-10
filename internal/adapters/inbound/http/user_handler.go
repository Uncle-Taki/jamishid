package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	userApp "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/user"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	domainUser "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/user"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/gin-gonic/gin"
)

type UserHandler struct {
	service      *userApp.Service
	tokenManager ports.TokenManager
}

func NewUserHandler(service *userApp.Service, tokenManager ports.TokenManager) *UserHandler {
	return &UserHandler{
		service:      service,
		tokenManager: tokenManager,
	}
}

type AuthRequest struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

type UserDTO struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	CreatedAt  string `json:"createdAt"`
}

type AuthResponse struct {
	User  UserDTO `json:"user"`
	Token string  `json:"token"`
}

type SaveListingRequest struct {
	Platform  string          `json:"platform"`
	ListingID string          `json:"listingId"`
	Listing   json.RawMessage `json:"listing,omitempty"`
}

func (h *UserHandler) Register(c *gin.Context) {
	var req AuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "درخواست نامعتبر است"})
		return
	}

	u, token, err := h.service.Register(c.Request.Context(), req.Identifier, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, domainUser.ErrInvalidIdentifier):
			c.JSON(http.StatusBadRequest, gin.H{"error": "نام کاربری یا ایمیل باید بین ۳ تا ۲۵۴ نویسه باشد"})
		case errors.Is(err, domainUser.ErrInvalidPassword):
			c.JSON(http.StatusBadRequest, gin.H{"error": "رمز عبور باید بین ۶ تا ۷۲ نویسه باشد"})
		case errors.Is(err, domainUser.ErrUserAlreadyExists):
			c.JSON(http.StatusConflict, gin.H{"error": "کاربری با این مشخصات قبلاً ثبت‌نام کرده است"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "خطا در ثبت‌نام"})
		}
		return
	}

	c.JSON(http.StatusOK, AuthResponse{
		User: UserDTO{
			ID:         u.ID,
			Identifier: u.Identifier,
			CreatedAt:  u.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		},
		Token: token,
	})
}

func (h *UserHandler) Login(c *gin.Context) {
	var req AuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "درخواست نامعتبر است"})
		return
	}

	u, token, err := h.service.Login(c.Request.Context(), req.Identifier, req.Password)
	if err != nil {
		if errors.Is(err, domainUser.ErrInvalidCredentials) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "نام کاربری یا رمز عبور اشتباه است"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "خطا در ورود"})
		return
	}

	c.JSON(http.StatusOK, AuthResponse{
		User: UserDTO{
			ID:         u.ID,
			Identifier: u.Identifier,
			CreatedAt:  u.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		},
		Token: token,
	})
}

func (h *UserHandler) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "ورود به حساب کاربری لازم است"})
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		userID, err := h.tokenManager.ValidateToken(tokenStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "نشست منقضی شده است؛ دوباره وارد شوید"})
			return
		}

		c.Set("userID", userID)
		c.Next()
	}
}

func (h *UserHandler) Me(c *gin.Context) {
	userID := c.GetString("userID")
	u, err := h.service.GetProfile(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "کاربر یافت نشد"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user": UserDTO{
			ID:         u.ID,
			Identifier: u.Identifier,
			CreatedAt:  u.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		},
	})
}

func (h *UserHandler) GetSavedListings(c *gin.Context) {
	userID := c.GetString("userID")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}

	items, total, err := h.service.GetSavedListings(c.Request.Context(), userID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "خطا در دریافت آگهی‌های نشان‌شده"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":   items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func (h *UserHandler) GetSavedListingIDs(c *gin.Context) {
	userID := c.GetString("userID")
	ids, err := h.service.GetSavedListingIDs(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "خطا در دریافت شناسه‌ها"})
		return
	}

	// Return as array of strings like ["divar:123", "bama:456"]
	list := make([]string, 0, len(ids))
	for k := range ids {
		list = append(list, k)
	}

	c.JSON(http.StatusOK, gin.H{
		"savedIds": list,
	})
}

func (h *UserHandler) SaveListing(c *gin.Context) {
	userID := c.GetString("userID")
	var req SaveListingRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Platform == "" || req.ListingID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "شناسه منبع و آگهی الزامی است"})
		return
	}

	err := h.service.SaveListing(c.Request.Context(), userID, vehicle.Platform(req.Platform), req.ListingID, req.Listing)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ذخیره آگهی ناموفق بود"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *UserHandler) RemoveSavedListing(c *gin.Context) {
	userID := c.GetString("userID")
	platform := c.Param("platform")
	id := c.Param("id")
	if platform == "" || id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "پارامترهای مسیر نامعتبر است"})
		return
	}

	err := h.service.RemoveSavedListing(c.Request.Context(), userID, vehicle.Platform(platform), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "حذف نشان آگهی ناموفق بود"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}
