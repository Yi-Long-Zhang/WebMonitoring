package jobqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/metrics"
	"webmonitoring/internal/security"
)

const (
	detectionStreamName  = "webmonitoring:detection-jobs"
	alertStreamName      = "webmonitoring:alert-jobs"
	deadLetterSuffix     = ":dead-letter"
	defaultMaxDeliveries = 5
)

type Redis struct {
	client            *redis.Client
	stream            string
	group             string
	maxDeliveries     int64
	minIdle           time.Duration
	deadLetterHandler func(context.Context, []byte, error) error
	metrics           *metrics.Registry
}

func NewRedis(ctx context.Context, address, group string) (*Redis, error) {
	return newStream(ctx, address, detectionStreamName, group)
}

func NewAlertRedis(ctx context.Context, address, group string) (*Redis, error) {
	return newStream(ctx, address, alertStreamName, group)
}

func newStream(ctx context.Context, address, stream, group string) (*Redis, error) {
	client := redis.NewClient(&redis.Options{Addr: address})
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("connect redis: %w", err)
	}
	if err := ensureGroup(ctx, client, stream, group); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &Redis{
		client: client, stream: stream, group: group,
		maxDeliveries: defaultMaxDeliveries, minIdle: 2 * time.Minute,
	}, nil
}

func ensureGroup(ctx context.Context, client *redis.Client, stream, group string) error {
	err := client.XGroupCreateMkStream(ctx, stream, group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("create redis consumer group: %w", err)
	}
	return nil
}

func (r *Redis) Close() error { return r.client.Close() }

func (r *Redis) Ready(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

func (r *Redis) Pending(ctx context.Context) (int64, error) {
	info, err := r.client.XPending(ctx, r.stream, r.group).Result()
	if err != nil {
		return 0, err
	}
	return info.Count, nil
}

func (r *Redis) StreamName() string {
	return r.stream
}

func (r *Redis) SetDeadLetterHandler(handler func(context.Context, []byte, error) error) {
	r.deadLetterHandler = handler
}

func (r *Redis) SetMaxDeliveries(value int64) {
	if value > 0 {
		r.maxDeliveries = value
	}
}

func (r *Redis) SetMinIdle(value time.Duration) {
	if value > 0 {
		r.minIdle = value
	}
}

func (r *Redis) SetMetrics(registry *metrics.Registry) {
	r.metrics = registry
}

func (r *Redis) Enqueue(ctx context.Context, job domain.DetectionJob) error {
	payload, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return r.enqueue(ctx, payload)
}

func (r *Redis) EnqueueAlert(ctx context.Context, job domain.AlertJob) error {
	payload, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return r.enqueue(ctx, payload)
}

func (r *Redis) enqueue(ctx context.Context, payload []byte) error {
	err := r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: r.stream,
		Values: map[string]any{"payload": string(payload)},
	}).Err()
	if r.metrics != nil {
		result := "succeeded"
		if err != nil {
			result = "failed"
		}
		r.metrics.Inc("webmonitoring_queue_enqueues_total", map[string]string{
			"stream": r.stream, "result": result,
		})
	}
	return err
}

func (r *Redis) Consume(ctx context.Context, consumer string, process func(context.Context, domain.DetectionJob) error) error {
	return r.consume(ctx, consumer, func(ctx context.Context, payload []byte) error {
		var job domain.DetectionJob
		if err := json.Unmarshal(payload, &job); err != nil {
			return fmt.Errorf("decode detection job: %w", err)
		}
		return process(ctx, job)
	})
}

func (r *Redis) ConsumeAlerts(ctx context.Context, consumer string, process func(context.Context, domain.AlertJob) error) error {
	return r.consume(ctx, consumer, func(ctx context.Context, payload []byte) error {
		var job domain.AlertJob
		if err := json.Unmarshal(payload, &job); err != nil {
			return fmt.Errorf("decode alert job: %w", err)
		}
		return process(ctx, job)
	})
}

func (r *Redis) consume(ctx context.Context, consumer string, process func(context.Context, []byte) error) error {
	for {
		claimed, _, err := r.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream: r.stream, Group: r.group, Consumer: consumer,
			MinIdle: r.minIdle, Start: "0-0", Count: 10,
		}).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			if r.metrics != nil {
				r.metrics.Inc("webmonitoring_queue_claim_failures_total", map[string]string{"stream": r.stream})
			}
			return fmt.Errorf("claim pending jobs from %s: %w", r.stream, err)
		}
		for _, message := range claimed {
			if err := r.processMessage(ctx, message, process); err != nil {
				if r.metrics != nil {
					r.metrics.Inc("webmonitoring_queue_processing_failures_total", map[string]string{"stream": r.stream})
				}
				continue
			}
		}
		if r.metrics != nil {
			if pending, pendingErr := r.Pending(ctx); pendingErr == nil {
				r.metrics.Set("webmonitoring_queue_pending", float64(pending), map[string]string{"stream": r.stream})
			}
		}

		streams, err := r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: r.group, Consumer: consumer, Streams: []string{r.stream, ">"},
			Count: 1, Block: 5 * time.Second,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if r.metrics != nil {
				r.metrics.Inc("webmonitoring_queue_read_failures_total", map[string]string{"stream": r.stream})
			}
			return fmt.Errorf("read jobs from %s: %w", r.stream, err)
		}
		for _, stream := range streams {
			for _, message := range stream.Messages {
				if err := r.processMessage(ctx, message, process); err != nil {
					if r.metrics != nil {
						r.metrics.Inc("webmonitoring_queue_processing_failures_total", map[string]string{"stream": r.stream})
					}
					continue
				}
			}
		}
		if r.metrics != nil {
			if pending, pendingErr := r.Pending(ctx); pendingErr == nil {
				r.metrics.Set("webmonitoring_queue_pending", float64(pending), map[string]string{"stream": r.stream})
			}
		}
	}
}

func (r *Redis) processMessage(ctx context.Context, message redis.XMessage, process func(context.Context, []byte) error) error {
	raw, ok := message.Values["payload"].(string)
	if !ok {
		return r.handleFailure(ctx, message, nil, errors.New("queue message has no payload"))
	}
	payload := []byte(raw)
	if err := process(ctx, payload); err != nil {
		if r.metrics != nil {
			r.metrics.Inc("webmonitoring_queue_deliveries_total", map[string]string{
				"stream": r.stream, "result": "failed",
			})
		}
		return r.handleFailure(ctx, message, payload, err)
	}
	if r.metrics != nil {
		r.metrics.Inc("webmonitoring_queue_deliveries_total", map[string]string{
			"stream": r.stream, "result": "succeeded",
		})
	}
	return r.ack(ctx, message.ID)
}

func (r *Redis) handleFailure(ctx context.Context, message redis.XMessage, payload []byte, cause error) error {
	if cause == nil {
		cause = errors.New("queue message processing failed")
	}
	deliveries, countErr := r.deliveryCount(ctx, message.ID)
	if countErr != nil {
		return fmt.Errorf("inspect delivery %s: %w", message.ID, countErr)
	}
	if deliveries < r.maxDeliveries {
		return fmt.Errorf("process %s message %s: %w", r.stream, message.ID, cause)
	}
	if r.metrics != nil {
		r.metrics.Inc("webmonitoring_queue_dead_letters_total", map[string]string{"stream": r.stream})
		r.metrics.Inc("webmonitoring_queue_deliveries_total", map[string]string{
			"stream": r.stream, "result": "dead_letter",
		})
	}
	if r.deadLetterHandler != nil {
		if handlerErr := r.deadLetterHandler(ctx, payload, cause); handlerErr != nil {
			return fmt.Errorf("persist dead-letter state for %s: %w", message.ID, handlerErr)
		}
	}
	if err := r.deadLetter(ctx, message, payload, cause, deliveries); err != nil {
		return err
	}
	return r.ack(ctx, message.ID)
}

func (r *Redis) deliveryCount(ctx context.Context, id string) (int64, error) {
	items, err := r.client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: r.stream, Group: r.group, Start: id, End: id, Count: 1,
	}).Result()
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, nil
	}
	return items[0].RetryCount, nil
}

func (r *Redis) deadLetter(ctx context.Context, message redis.XMessage, payload []byte, cause error, deliveries int64) error {
	return r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: r.stream + deadLetterSuffix,
		Values: map[string]any{
			"original_id": message.ID,
			"payload":     string(payload),
			"error":       safeError(cause),
			"attempts":    deliveries,
		},
	}).Err()
}

func (r *Redis) ack(ctx context.Context, id string) error {
	if err := r.client.XAck(ctx, r.stream, r.group, id).Err(); err != nil {
		return fmt.Errorf("ack job %s: %w", id, err)
	}
	return nil
}

func safeError(err error) string {
	message := security.RedactString(err.Error())
	if len(message) > 240 {
		return message[:240]
	}
	return message
}
