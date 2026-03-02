package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"trade-lab/internal/api"
	"trade-lab/internal/eventbus"
	"trade-lab/internal/risk"
)

func setupTestAPI() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	bus := eventbus.NewInMemoryBus(1000)
	riskState := risk.NewState()
	riskCtrl := risk.NewController(riskState, bus)
	handlers := api.NewHandlers(bus, riskCtrl)

	v1 := router.Group("/api/v1")
	handlers.RegisterRoutes(v1)

	return router
}

func TestListStrategies(t *testing.T) {
	router := setupTestAPI()

	req, _ := http.NewRequest("GET", "/api/v1/strategies", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Errorf("failed to parse response: %v", err)
	}

	strategies, ok := resp["strategies"].([]interface{})
	if !ok {
		t.Error("expected strategies array")
	}
	if len(strategies) != 3 {
		t.Errorf("expected 3 strategies, got %d", len(strategies))
	}
}

func TestListTasks(t *testing.T) {
	router := setupTestAPI()

	req, _ := http.NewRequest("GET", "/api/v1/tasks", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestCreateTask(t *testing.T) {
	router := setupTestAPI()

	req, _ := http.NewRequest("POST", "/api/v1/tasks", nil)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Should fail without proper body, but test routing works
	if w.Code != http.StatusBadRequest && w.Code != http.StatusCreated {
		t.Errorf("unexpected status: %d", w.Code)
	}
}

func TestGetRiskStatus(t *testing.T) {
	router := setupTestAPI()

	req, _ := http.NewRequest("GET", "/api/v1/risk/status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Errorf("failed to parse response: %v", err)
	}

	if _, ok := resp["circuit_breaker"]; !ok {
		t.Error("expected circuit_breaker field")
	}
}

func TestGetDashboardSummary(t *testing.T) {
	router := setupTestAPI()

	req, _ := http.NewRequest("GET", "/api/v1/dashboard/summary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Errorf("failed to parse response: %v", err)
	}

	if _, ok := resp["total_tasks"]; !ok {
		t.Error("expected total_tasks field")
	}
}

func TestGetAuditLog(t *testing.T) {
	router := setupTestAPI()

	req, _ := http.NewRequest("GET", "/api/v1/risk/audit", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}
