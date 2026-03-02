package health

import (
	"net/http"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
)

// Status represents health check status
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusUnhealthy Status = "unhealthy"
)

// Response is the health check response
type Response struct {
	Status    Status `json:"status"`
	Timestamp string `json:"timestamp"`
	Version   string `json:"version"`
	Uptime    string `json:"uptime"`
	GoVersion string `json:"go_version"`
	Goroutines int   `json:"goroutines"`
}

var (
	startTime = time.Now()
	version   = "0.1.0"
)

// Handler returns health check handler
func Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		resp := Response{
			Status:    StatusHealthy,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Version:   version,
			Uptime:    time.Since(startTime).Round(time.Second).String(),
			GoVersion: runtime.Version(),
			Goroutines: runtime.NumGoroutine(),
		}
		c.JSON(http.StatusOK, resp)
	}
}

// ReadyHandler returns readiness check handler
func ReadyHandler(checks ...func() error) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, check := range checks {
			if err := check(); err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{
					"status": StatusUnhealthy,
					"error":  err.Error(),
				})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": StatusHealthy})
	}
}
