package redisqueue

import (
	"context"
	"testing"
	"time"

	"goodshunter/proto/pb"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestClient_TaskFlow(t *testing.T) {
	// 1. 启动 Mock Redis
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	// 2. 初始化 Client
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	// 3. 测试 PushTask
	req := &pb.FetchRequest{
		TaskId:  "1001",
		Keyword: "PS5",
		// 修正：使用生成的正确枚举名称
		Platform: pb.Platform_PLATFORM_MERCARI,
		MinPrice: 1000,
		MaxPrice: 5000,
	}

	if err := client.PushTask(ctx, req); err != nil {
		t.Errorf("PushTask failed: %v", err)
	}

	// 验证队列长度
	tasks, results, err := client.QueueDepth(ctx)
	if err != nil {
		t.Errorf("QueueDepth failed: %v", err)
	}
	if tasks != 1 || results != 0 {
		t.Errorf("expected 1 task, 0 results, got %d tasks, %d results", tasks, results)
	}

	// 4. 测试 PopTask
	poppedReq, err := client.PopTask(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopTask failed: %v", err)
	}

	if poppedReq.TaskId != req.TaskId || poppedReq.Keyword != req.Keyword {
		t.Errorf("PopTask data mismatch. expected %v, got %v", req, poppedReq)
	}
}

func TestClient_ResultFlow(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, _ := NewClientWithRedis(rdb)
	ctx := context.Background()

	// 1. 测试 PushResult
	res := &pb.FetchResponse{
		TaskId:       "1001",
		TotalFound:   5,
		ErrorMessage: "",
		Items: []*pb.Item{
			{Title: "Sony PS5", Price: 45000, SourceId: "m123456"},
		},
	}

	if err := client.PushResult(ctx, res); err != nil {
		t.Errorf("PushResult failed: %v", err)
	}

	// 2. 测试 PopResult
	poppedRes, err := client.PopResult(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopResult failed: %v", err)
	}

	if len(poppedRes.Items) != 1 {
		t.Errorf("expected 1 item, got %d", len(poppedRes.Items))
	}
	if poppedRes.Items[0].SourceId != "m123456" {
		t.Errorf("item data mismatch")
	}
}
