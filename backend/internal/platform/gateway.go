package platform

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/url"
	"strconv"
)

// Production accepts requests only on the fnOS gateway Unix Socket.
func GatewayRequired(development bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			if origin := c.GetHeader("Origin"); origin != "" {
				parsed, err := url.Parse(origin)
				if err != nil || parsed.Host != c.Request.Host {
					c.AbortWithStatusJSON(403, gin.H{"code": 403, "msg": "不允许跨域操作"})
					return
				}
			}
			if c.GetHeader("Sec-Fetch-Site") == "cross-site" {
				c.AbortWithStatusJSON(403, gin.H{"code": 403, "msg": "不允许跨站操作"})
				return
			}
		}
		if development {
			c.Set("uid", int64(1000))
			c.Next()
			return
		}
		uid, err := strconv.ParseInt(c.GetHeader("X-Trim-Userid"), 10, 64)
		if err != nil || uid <= 0 {
			c.AbortWithStatusJSON(401, gin.H{"code": 401, "msg": "请从飞牛桌面打开应用"})
			return
		}
		if c.GetHeader("X-Trim-Isadmin") != "true" {
			c.AbortWithStatusJSON(403, gin.H{"code": 403, "msg": "仅管理员可使用此应用"})
			return
		}
		c.Set("uid", uid)
		c.Next()
	}
}
