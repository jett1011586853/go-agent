package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"trade-lab/internal/backtest"
	"trade-lab/internal/eventbus"
	"trade-lab/internal/risk"
	"trade-lab/internal/strategy"
)

// Handlers holds API dependencies
type Handlers struct {
	bus         eventbus.Bus
	riskCtrl    *risk.Controller
	strategies  map[string]strategy.Strategy
	tasks       map[string]*Task
	taskCounter int
}

// Task represents a backtest task
type Task struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"` // pending, running, completed, failed
	Strategy  string    `json:"strategy"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time,omitempty"`
	Report    *backtest.Report `json:"report,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// NewHandlers creates API handlers
func NewHandlers(bus eventbus.Bus, riskCtrl *risk.Controller) *Handlers {
	return &Handlers{
		bus:        bus,
		riskCtrl:   riskCtrl,
		strategies: make(map[string]strategy.Strategy),
		tasks:      make(map[string]*Task),
	}
}

// RegisterRoutes registers API routes
func (h *Handlers) RegisterRoutes(r *gin.RouterGroup) {
	// Strategy endpoints
	r.GET("/strategies", h.listStrategies)
	r.POST("/strategies/:name/start", h.startStrategy)
	r.POST("/strategies/:name/stop", h.stopStrategy)

	// Task endpoints
	r.GET("/tasks", h.listTasks)
	r.GET("/tasks/:id", h.getTask)
	r.POST("/tasks", h.createTask)
	r.DELETE("/tasks/:id", h.deleteTask)

	// Report endpoints
	r.GET("/reports", h.listReports)
	r.GET("/reports/:id", h.getReport)
	r.GET("/reports/:id/export", h.exportReport)

	// Risk endpoints
	r.GET("/risk/status", h.getRiskStatus)
	r.GET("/risk/audit", h.getAuditLog)

	// Dashboard endpoints
	r.GET("/dashboard/summary", h.getDashboardSummary)
}

// ListStrategiesResponse lists available strategies
func (h *Handlers) listStrategies(c *gin.Context) {
	strategies := []gin.H{
		{"name": "sma", "type": "trend", "params": []string{"short_period", "long_period"}},
		{"name": "bollinger", "type": "mean_reversion", "params": []string{"period", "std_dev"}},
		{"name": "momentum", "type": "momentum", "params": []string{"period"}},
	}
	c.JSON(http.StatusOK, gin.H{"strategies": strategies})
}

func (h *Handlers) startStrategy(c *gin.Context) {
	name := c.Param("name")
	c.JSON(http.StatusOK, gin.H{"message": "strategy started", "name": name})
}

func (h *Handlers) stopStrategy(c *gin.Context) {
	name := c.Param("name")
	c.JSON(http.StatusOK, gin.H{"message": "strategy stopped", "name": name})
}

func (h *Handlers) listTasks(c *gin.Context) {
	tasks := make([]gin.H, 0, len(h.tasks))
	for _, t := range h.tasks {
		tasks = append(tasks, gin.H{
			"id":     t.ID,
			"status": t.Status,
			"strategy": t.Strategy,
		})
	}
	c.JSON(http.StatusOK, gin.H{"tasks": tasks})
}

func (h *Handlers) getTask(c *gin.Context) {
	id := c.Param("id")
	task, ok := h.tasks[id]
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	c.JSON(http.StatusOK, task)
}

// CreateTaskRequest is the request body for creating a task
type CreateTaskRequest struct {
	Strategy string                 `json:"strategy"`
	Params   map[string]interface{} `json:"params"`
}

func (h *Handlers) createTask(c *gin.Context) {
	var req CreateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.taskCounter++
	id := fmt.Sprintf("task-%d", h.taskCounter)
	task := &Task{
		ID:        id,
		Status:    "pending",
		Strategy:  req.Strategy,
		StartTime: time.Now(),
	}
	h.tasks[id] = task

	c.JSON(http.StatusCreated, task)
}

func (h *Handlers) deleteTask(c *gin.Context) {
	id := c.Param("id")
	delete(h.tasks, id)
	c.JSON(http.StatusOK, gin.H{"message": "task deleted"})
}

func (h *Handlers) listReports(c *gin.Context) {
	reports := []gin.H{}
	for _, t := range h.tasks {
		if t.Report != nil {
			reports = append(reports, gin.H{
				"id":     t.ID,
				"status": t.Status,
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"reports": reports})
}

func (h *Handlers) getReport(c *gin.Context) {
	id := c.Param("id")
	task, ok := h.tasks[id]
	if !ok || task.Report == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "report not found"})
		return
	}
	c.JSON(http.StatusOK, task.Report)
}

func (h *Handlers) exportReport(c *gin.Context) {
	id := c.Param("id")
	format := c.DefaultQuery("format", "json")

	task, ok := h.tasks[id]
	if !ok || task.Report == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "report not found"})
		return
	}

	switch format {
	case "md":
		c.Header("Content-Disposition", "attachment; filename=report-"+id+".md")
		c.Data(http.StatusOK, "text/markdown", []byte(task.Report.ToMarkdown()))
	default:
		c.JSON(http.StatusOK, task.Report)
	}
}

func (h *Handlers) getRiskStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"circuit_breaker": false,
		"daily_pnl":       0,
		"position_value":  0,
	})
}

func (h *Handlers) getAuditLog(c *gin.Context) {
	log := h.riskCtrl.GetAuditLog()
	c.JSON(http.StatusOK, gin.H{"audit_log": log})
}

func (h *Handlers) getDashboardSummary(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"total_tasks":     len(h.tasks),
		"running_tasks":   0,
		"completed_tasks": 0,
		"strategies":      3,
		"risk_status":     "normal",
	})
}
