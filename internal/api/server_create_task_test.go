package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"goodshunter/internal/config"
	"goodshunter/internal/model"
	"goodshunter/internal/pkg/metrics"

	"github.com/gin-gonic/gin"
)

type mockTaskStore struct {
	countTasksFunc func(ctx context.Context, userID int) (int64, error)
	createTaskFunc func(ctx context.Context, task *model.Task) error
	countCalls     int
	createCalls    int
}

func (m *mockTaskStore) CountTasks(ctx context.Context, userID int) (int64, error) {
	m.countCalls++
	return m.countTasksFunc(ctx, userID)
}

func (m *mockTaskStore) CreateTask(ctx context.Context, task *model.Task) error {
	m.createCalls++
	return m.createTaskFunc(ctx, task)
}

type mockPublisher struct {
	submitFunc func(ctx context.Context, taskID uint, source string) error
	calls      int
}

func (m *mockPublisher) SubmitTask(ctx context.Context, taskID uint, source string) error {
	m.calls++
	return m.submitFunc(ctx, taskID, source)
}

type mockScheduler struct {
	startCalls int
}

func (m *mockScheduler) StartTask(ctx context.Context, task *model.Task) {
	m.startCalls++
}

type mockDeduper struct {
	dupFunc    func(ctx context.Context, url string) (bool, error)
	deleteFunc func(ctx context.Context, url string) error
	calls      int
}

func (m *mockDeduper) IsDuplicate(ctx context.Context, url string) (bool, error) {
	m.calls++
	return m.dupFunc(ctx, url)
}

func (m *mockDeduper) Delete(ctx context.Context, url string) error {
	if m.deleteFunc != nil {
		return m.deleteFunc(ctx, url)
	}
	return nil
}

func TestCreateTask_Normal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	metrics.InitMetrics(false, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	store := &mockTaskStore{
		countTasksFunc: func(ctx context.Context, userID int) (int64, error) { return 0, nil },
		createTaskFunc: func(ctx context.Context, task *model.Task) error {
			task.ID = 1
			return nil
		},
	}
	pub := &mockPublisher{submitFunc: func(ctx context.Context, taskID uint, source string) error { return nil }}
	sched := &mockScheduler{}
	deduper := &mockDeduper{dupFunc: func(ctx context.Context, url string) (bool, error) { return false, nil }}

	s := &Server{
		cfg:           &config.Config{App: config.AppConfig{MaxTasksPerUser: 3, EnableRedisQueue: true}},
		logger:        logger,
		taskStore:     store,
		taskPublisher: pub,
		taskScheduler: sched,
		deduper:       deduper,
	}

	r := gin.New()
	r.POST("/tasks", func(c *gin.Context) {
		c.Set("userID", 1)
		c.Set("role", "admin")
		s.handleCreateTask(c)
	})

	body := createTaskRequest{
		Keyword:  "nike",
		MinPrice: 1000,
		MaxPrice: 5000,
		Platform: 1,
		Sort:     "created_time",
		Status:   "running",
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	if store.createCalls != 1 {
		t.Fatalf("expected create task to be called")
	}
	if pub.calls != 1 {
		t.Fatalf("expected publish to be called")
	}
}

func TestCreateTask_Deduplicated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	metrics.InitMetrics(false, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	store := &mockTaskStore{
		countTasksFunc: func(ctx context.Context, userID int) (int64, error) { return 0, nil },
		createTaskFunc: func(ctx context.Context, task *model.Task) error { return nil },
	}
	pub := &mockPublisher{submitFunc: func(ctx context.Context, taskID uint, source string) error { return nil }}
	sched := &mockScheduler{}
	deduper := &mockDeduper{dupFunc: func(ctx context.Context, url string) (bool, error) { return true, nil }}

	s := &Server{
		cfg:           &config.Config{App: config.AppConfig{MaxTasksPerUser: 3, EnableRedisQueue: true}},
		logger:        logger,
		taskStore:     store,
		taskPublisher: pub,
		taskScheduler: sched,
		deduper:       deduper,
	}

	r := gin.New()
	r.POST("/tasks", func(c *gin.Context) {
		c.Set("userID", 1)
		c.Set("role", "admin")
		s.handleCreateTask(c)
	})

	body := createTaskRequest{
		Keyword:  "nike",
		Platform: 1,
		Sort:     "created_time",
		Status:   "running",
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if store.createCalls != 0 || pub.calls != 0 {
		t.Fatalf("expected no create/publish on dedup")
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("skipped_duplicate")) {
		t.Fatalf("expected skipped_duplicate in response body")
	}
}

func TestCreateTask_InvalidBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	metrics.InitMetrics(false, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	store := &mockTaskStore{
		countTasksFunc: func(ctx context.Context, userID int) (int64, error) { return 0, nil },
		createTaskFunc: func(ctx context.Context, task *model.Task) error { return nil },
	}
	pub := &mockPublisher{submitFunc: func(ctx context.Context, taskID uint, source string) error { return nil }}
	sched := &mockScheduler{}
	deduper := &mockDeduper{dupFunc: func(ctx context.Context, url string) (bool, error) { return false, nil }}

	s := &Server{
		cfg:           &config.Config{App: config.AppConfig{MaxTasksPerUser: 3, EnableRedisQueue: true}},
		logger:        logger,
		taskStore:     store,
		taskPublisher: pub,
		taskScheduler: sched,
		deduper:       deduper,
	}

	r := gin.New()
	r.POST("/tasks", func(c *gin.Context) {
		c.Set("userID", 1)
		c.Set("role", "admin")
		s.handleCreateTask(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewReader([]byte("{")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
