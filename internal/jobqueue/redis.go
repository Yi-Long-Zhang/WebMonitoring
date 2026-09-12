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
)

const streamName = "webmonitoring:detection-jobs"

type Redis struct {
	client *redis.Client
	group  string
}

func NewRedis(ctx context.Context, address, group string) (*Redis, error) {
	client := redis.NewClient(&redis.Options{Addr: address})
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("connect redis: %w", err)
	}
	err := client.XGroupCreateMkStream(ctx, streamName, group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return nil, fmt.Errorf("create redis consumer group: %w", err)
	}
	return &Redis{client: client, group: group}, nil
}

func (r *Redis) Close() error { return r.client.Close() }

func (r *Redis) Enqueue(ctx context.Context, job domain.DetectionJob) error {
	payload, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: streamName,
		Values: map[string]any{"job": string(payload)},
	}).Err()
}

func (r *Redis) Consume(ctx context.Context, consumer string, process func(context.Context, domain.DetectionJob)) error {
	for {
		claimed, _, err := r.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream: streamName, Group: r.group, Consumer: consumer,
			MinIdle: 30 * time.Second, Start: "0-0", Count: 10,
		}).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return fmt.Errorf("claim pending detection jobs: %w", err)
		}
		for _, message := range claimed {
			if err := r.processMessage(ctx, message, process); err != nil {
				return err
			}
		}

		streams, err := r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: r.group, Consumer: consumer, Streams: []string{streamName, ">"},
			Count: 1, Block: 5 * time.Second,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return err
		}
		for _, stream := range streams {
			for _, message := range stream.Messages {
				if err := r.processMessage(ctx, message, process); err != nil {
					return err
				}
			}
		}
	}
}

func (r *Redis) processMessage(ctx context.Context, message redis.XMessage, process func(context.Context, domain.DetectionJob)) error {
	raw, ok := message.Values["job"].(string)
	if !ok {
		return r.ack(ctx, message.ID)
	}
	var job domain.DetectionJob
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		return r.ack(ctx, message.ID)
	}
	process(ctx, job)
	return r.ack(ctx, message.ID)
}

func (r *Redis) ack(ctx context.Context, id string) error {
	if err := r.client.XAck(ctx, streamName, r.group, id).Err(); err != nil {
		return fmt.Errorf("ack detection job %s: %w", id, err)
	}
	return nil
}
