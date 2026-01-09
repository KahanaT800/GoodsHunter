# Task Queue Package

Redis Streams based task queue for GoodsHunter scheduler.

## Features

- ✅ Persistent task queue (Redis Streams with AOF/RDB)
- ✅ Consumer Group support for distributed scheduling
- ✅ Producer/Consumer pattern
- ✅ Graceful shutdown with ACK mechanism
- ✅ Error handling and retry support

## Usage

### Producer (API Service)

```go
import "goodshunter/internal/pkg/taskqueue"

// Create producer
producer := taskqueue.NewProducer(redisClient, logger)

// Submit task
err := producer.SubmitTask(ctx, taskID, "user_create")
```

### Consumer (Scheduler Service)

```go
// Create consumer
consumer, err := taskqueue.NewConsumer(
    redisClient, 
    logger, 
    "goodshunter:task:queue",
    "scheduler_group",
    "consumer-1",
)

// Read messages
messages, err := consumer.Read(ctx)
for _, msg := range messages {
    // Process task
    handleTask(msg.Message.TaskID)
    
    // Acknowledge after successful processing
    consumer.Ack(ctx, msg.ID)
}
```

## Architecture

```
API Service (Producer)
     ↓
Redis Streams (goodshunter:task:queue)
     ↓
Scheduler (Consumer Group: scheduler_group)
     ↓
Internal Worker Pool
     ↓
Crawler gRPC Service
```

## Configuration

Set in `config.json`:

```json
{
  "app": {
    "enable_redis_queue": true,
    "task_queue_stream": "goodshunter:task:queue",
    "task_queue_group": "scheduler_group"
  }
}
```

Or via environment variables:

```bash
export APP_ENABLE_REDIS_QUEUE=true
export APP_TASK_QUEUE_STREAM=goodshunter:task:queue
export APP_TASK_QUEUE_GROUP=scheduler_group
```

## Message Format

```go
type TaskMessage struct {
    TaskID    uint      `json:"task_id"`    // Task ID
    Action    string    `json:"action"`     // "execute" or "stop"
    Timestamp time.Time `json:"timestamp"`  // Message creation time
    Retry     int       `json:"retry"`      // Retry count
    Source    string    `json:"source"`     // "user_create" or "periodic"
}
```

## Redis Commands

```bash
# Check queue length
redis-cli XLEN goodshunter:task:queue

# View consumer group info
redis-cli XINFO GROUPS goodshunter:task:queue

# Check pending messages
redis-cli XPENDING goodshunter:task:queue scheduler_group

# Read latest messages
redis-cli XREAD COUNT 10 STREAMS goodshunter:task:queue 0
```

## Performance

- Non-blocking enqueue
- Batch processing support
- Low latency (<10ms per message)
- Horizontal scaling ready

## See Also

- [Redis Streams Upgrade Guide](../../docs/REDIS_STREAMS_UPGRADE.md)
- [Worker Pool Integration](../../docs/WORKER_POOL_INTEGRATION.md)
