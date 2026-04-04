package middleware

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID int64  `json:"userId"`
	Role   string `json:"role"`
	CityID *int64 `json:"cityId,omitempty"`
	jwt.RegisteredClaims
}

// Auth 验证 JWT，将 claims 注入 gin.Context
func Auth(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}
		tokenStr := strings.TrimPrefix(header, "Bearer ")

		claims := &Claims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(jwtSecret), nil
		})
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}

		c.Set("userID", claims.UserID)
		c.Set("role", claims.Role)
		c.Set("cityID", claims.CityID)
		c.Next()
	}
}

// RequireSuperAdmin 限制只有 super_admin 可访问
func RequireSuperAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if role != "super_admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "super admin only"})
			return
		}
		c.Next()
	}
}

// CityScope 确保 city_admin 只能操作自己的地市；observer 禁止访问所有地市路由
func CityScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if role == "observer" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "observers have read-only dashboard access"})
			return
		}
		if role == "super_admin" {
			c.Next()
			return
		}

		jwtCityID, _ := c.Get("cityID")
		paramCityID := c.Param("cityId")
		if paramCityID != "" && jwtCityID != nil {
			if fmt.Sprintf("%v", jwtCityID) != paramCityID {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "access denied for this city"})
				return
			}
		}
		c.Next()
	}
}
