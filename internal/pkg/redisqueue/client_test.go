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

// ============================================================================
// 去重功能测试
// ============================================================================

func TestClient_Deduplication(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.FetchRequest{
		TaskId:   "dedup-test-1",
		Keyword:  "test",
		Platform: pb.Platform_PLATFORM_MERCARI,
	}

	// 第一次推送应该成功
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("first PushTask should succeed: %v", err)
	}

	// 验证 pending set 大小
	size, err := client.PendingSetSize(ctx)
	if err != nil {
		t.Fatalf("PendingSetSize failed: %v", err)
	}
	if size != 1 {
		t.Errorf("expected pending set size 1, got %d", size)
	}

	// 第二次推送相同任务应该返回 ErrTaskExists
	err = client.PushTask(ctx, req)
	if err != ErrTaskExists {
		t.Errorf("second PushTask should return ErrTaskExists, got: %v", err)
	}

	// 队列长度应该还是 1（没有重复入队）
	tasks, _, err := client.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("QueueDepth failed: %v", err)
	}
	if tasks != 1 {
		t.Errorf("expected 1 task in queue, got %d", tasks)
	}
}

func TestClient_AckTask_ClearsPendingSet(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.FetchRequest{
		TaskId:    "ack-test-1",
		Keyword:   "test",
		Platform:  pb.Platform_PLATFORM_MERCARI,
		CreatedAt: time.Now().Unix(),
	}

	// 1. 推送任务
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("PushTask failed: %v", err)
	}

	// 2. 弹出任务（模拟 Crawler 消费）
	popped, err := client.PopTask(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopTask failed: %v", err)
	}

	// 此时 pending set 应该还有 1 个
	size, _ := client.PendingSetSize(ctx)
	if size != 1 {
		t.Errorf("after PopTask, pending set size should be 1, got %d", size)
	}

	// 3. Ack 任务（模拟任务完成）
	if err := client.AckTask(ctx, popped); err != nil {
		t.Fatalf("AckTask failed: %v", err)
	}

	// 4. Ack 后 pending set 应该清空
	size, _ = client.PendingSetSize(ctx)
	if size != 0 {
		t.Errorf("after AckTask, pending set size should be 0, got %d", size)
	}

	// 5. 再次推送相同任务应该成功（因为已经被 Ack）
	if err := client.PushTask(ctx, req); err != nil {
		t.Errorf("PushTask after AckTask should succeed, got: %v", err)
	}
}

func TestClient_NoAck_BlocksRePush(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.FetchRequest{
		TaskId:    "no-ack-test-1",
		Keyword:   "test",
		Platform:  pb.Platform_PLATFORM_MERCARI,
		CreatedAt: time.Now().Unix(),
	}

	// 1. 推送任务
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("PushTask failed: %v", err)
	}

	// 2. 弹出任务（模拟 Crawler 消费）
	_, err = client.PopTask(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopTask failed: %v", err)
	}

	// 3. 不调用 AckTask（模拟任务超时）

	// 4. 尝试再次推送相同任务应该被阻止
	err = client.PushTask(ctx, req)
	if err != ErrTaskExists {
		t.Errorf("PushTask without AckTask should return ErrTaskExists, got: %v", err)
	}

	// 5. pending set 应该仍然有 1 个
	size, _ := client.PendingSetSize(ctx)
	if size != 1 {
		t.Errorf("pending set size should be 1, got %d", size)
	}
}

func TestClient_MultipleTasksDeduplication(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	// 模拟 10 个不同的任务（使用固定的 TaskId）
	taskIDs := make([]string, 10)
	for i := 0; i < 10; i++ {
		taskIDs[i] = "multi-task-" + string(rune('A'+i))
		req := &pb.FetchRequest{
			TaskId:   taskIDs[i],
			Keyword:  "test",
			Platform: pb.Platform_PLATFORM_MERCARI,
		}
		if err := client.PushTask(ctx, req); err != nil {
			t.Fatalf("PushTask %d failed: %v", i, err)
		}
	}

	// 队列应该有 10 个任务
	tasks, _, err := client.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("QueueDepth failed: %v", err)
	}
	if tasks != 10 {
		t.Errorf("expected 10 tasks, got %d", tasks)
	}

	// pending set 也应该有 10 个
	size, _ := client.PendingSetSize(ctx)
	if size != 10 {
		t.Errorf("expected pending set size 10, got %d", size)
	}

	// 模拟调度器再次推送这 10 个任务（应该全部被阻止）
	for i := 0; i < 10; i++ {
		req := &pb.FetchRequest{
			TaskId:   taskIDs[i],
			Keyword:  "test",
			Platform: pb.Platform_PLATFORM_MERCARI,
		}
		err := client.PushTask(ctx, req)
		if err != ErrTaskExists {
			t.Errorf("re-push task %s should return ErrTaskExists, got: %v", taskIDs[i], err)
		}
	}

	// 验证队列长度没有增长
	tasks, _, _ = client.QueueDepth(ctx)
	if tasks != 10 {
		t.Errorf("queue length should remain 10, got %d", tasks)
	}
}

func TestClient_RescueStuckTasks_PreservesPendingSet(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.FetchRequest{
		TaskId:    "stuck-task-1",
		Keyword:   "test",
		Platform:  pb.Platform_PLATFORM_MERCARI,
		CreatedAt: time.Now().Unix(),
	}

	// 1. 推送任务
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("PushTask failed: %v", err)
	}

	// 2. 弹出任务（移到 processing queue，同时会记录开始时间到 started hash）
	popped, err := client.PopTask(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopTask failed: %v", err)
	}

	// 验证任务在 processing queue
	procLen, err := rdb.LLen(ctx, KeyTaskProcessingQueue).Result()
	if err != nil {
		t.Fatalf("LLen failed: %v", err)
	}
	if procLen != 1 {
		t.Errorf("expected 1 task in processing queue, got %d", procLen)
	}

	// 3. 手动修改 started hash 中的时间为 1 小时前（模拟任务卡住）
	stuckTime := time.Now().Add(-1 * time.Hour).Unix()
	if err := rdb.HSet(ctx, KeyTaskStartedHash, "stuck-task-1", stuckTime).Err(); err != nil {
		t.Fatalf("HSet started time failed: %v", err)
	}

	// 4. 执行 RescueStuckTasks（超时阈值设为 30 分钟，小于 1 小时）
	rescued, err := client.RescueStuckTasks(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("RescueStuckTasks failed: %v", err)
	}
	if rescued != 1 {
		t.Errorf("expected 1 rescued task, got %d", rescued)
	}

	// 5. 验证任务回到 task queue
	tasks, _, err := client.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("QueueDepth failed: %v", err)
	}
	if tasks != 1 {
		t.Errorf("expected 1 task in queue after rescue, got %d", tasks)
	}

	// 6. 关键验证：pending set 应该仍然有这个任务
	size, _ := client.PendingSetSize(ctx)
	if size != 1 {
		t.Errorf("pending set should still have 1 task after rescue, got %d", size)
	}

	// 7. 验证 started hash 已被清理
	exists, err := rdb.HExists(ctx, KeyTaskStartedHash, "stuck-task-1").Result()
	if err != nil {
		t.Fatalf("HExists failed: %v", err)
	}
	if exists {
		t.Errorf("started hash should be cleared after rescue")
	}

	// 8. 再次推送相同任务应该被阻止（即使任务被 rescue 了）
	err = client.PushTask(ctx, popped)
	if err != ErrTaskExists {
		t.Errorf("PushTask after rescue should return ErrTaskExists, got: %v", err)
	}
}

func TestClient_RescueDoesNotAffectActiveTask(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.FetchRequest{
		TaskId:    "active-task-1",
		Keyword:   "test",
		Platform:  pb.Platform_PLATFORM_MERCARI,
		CreatedAt: time.Now().Unix(),
	}

	// 1. 推送任务
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("PushTask failed: %v", err)
	}

	// 2. 弹出任务（模拟 worker 开始处理）
	_, err = client.PopTask(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopTask failed: %v", err)
	}

	// 3. 不修改 started time（保持刚刚开始的状态）

	// 4. 执行 RescueStuckTasks（超时阈值设为 30 分钟）
	// 因为任务刚开始处理，不应该被 rescue
	rescued, err := client.RescueStuckTasks(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("RescueStuckTasks failed: %v", err)
	}
	if rescued != 0 {
		t.Errorf("expected 0 rescued tasks (task is active), got %d", rescued)
	}

	// 5. 验证任务仍在 processing queue
	procLen, err := rdb.LLen(ctx, KeyTaskProcessingQueue).Result()
	if err != nil {
		t.Fatalf("LLen failed: %v", err)
	}
	if procLen != 1 {
		t.Errorf("expected 1 task still in processing queue, got %d", procLen)
	}

	// 6. task queue 应该是空的
	tasks, _, err := client.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("QueueDepth failed: %v", err)
	}
	if tasks != 0 {
		t.Errorf("expected 0 tasks in queue, got %d", tasks)
	}
}

func TestClient_StartedHashClearedOnAck(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.FetchRequest{
		TaskId:    "started-hash-test-1",
		Keyword:   "test",
		Platform:  pb.Platform_PLATFORM_MERCARI,
		CreatedAt: time.Now().Unix(),
	}

	// 1. 推送任务
	if err := client.PushTask(ctx, req); err != nil {
		t.Fatalf("PushTask failed: %v", err)
	}

	// 2. 弹出任务
	popped, err := client.PopTask(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PopTask failed: %v", err)
	}

	// 3. 验证 started hash 有记录
	exists, err := rdb.HExists(ctx, KeyTaskStartedHash, "started-hash-test-1").Result()
	if err != nil {
		t.Fatalf("HExists failed: %v", err)
	}
	if !exists {
		t.Errorf("started hash should have record after PopTask")
	}

	// 4. Ack 任务
	if err := client.AckTask(ctx, popped); err != nil {
		t.Fatalf("AckTask failed: %v", err)
	}

	// 5. 验证 started hash 已清理
	exists, err = rdb.HExists(ctx, KeyTaskStartedHash, "started-hash-test-1").Result()
	if err != nil {
		t.Fatalf("HExists failed: %v", err)
	}
	if exists {
		t.Errorf("started hash should be cleared after AckTask")
	}
}

func TestClient_TimeoutWithoutAck_QueueDoesNotGrow(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client, err := NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	req := &pb.FetchRequest{
		TaskId:    "timeout-test-1",
		Keyword:   "test",
		Platform:  pb.Platform_PLATFORM_MERCARI,
		CreatedAt: time.Now().Unix(),
	}

	// 模拟多轮调度（爬虫卡死场景）
	for round := 1; round <= 5; round++ {
		// 调度器尝试推送
		err := client.PushTask(ctx, req)

		if round == 1 {
			// 第一轮应该成功
			if err != nil {
				t.Fatalf("round %d: first PushTask should succeed: %v", round, err)
			}
		} else {
			// 后续轮次应该被阻止
			if err != ErrTaskExists {
				t.Errorf("round %d: PushTask should return ErrTaskExists, got: %v", round, err)
			}
		}
	}

	// 验证队列长度始终为 1（不会因为多轮调度而增长）
	tasks, _, err := client.QueueDepth(ctx)
	if err != nil {
		t.Fatalf("QueueDepth failed: %v", err)
	}
	if tasks != 1 {
		t.Errorf("queue should have exactly 1 task, got %d", tasks)
	}

	// pending set 也应该只有 1 个
	size, _ := client.PendingSetSize(ctx)
	if size != 1 {
		t.Errorf("pending set should have exactly 1 task, got %d", size)
	}
}
