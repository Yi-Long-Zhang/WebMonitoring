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
				raw, ok := message.Values["job"].(string)
				if !ok {
					_ = r.client.XAck(ctx, streamName, r.group, message.ID).Err()
					continue
				}
				var job domain.DetectionJob
				if json.Unmarshal([]byte(raw), &job) != nil {
					_ = r.client.XAck(ctx, streamName, r.group, message.ID).Err()
					continue
				}
				process(ctx, job)
				if err := r.client.XAck(ctx, streamName, r.group, message.ID).Err(); err != nil {
					return err
				}
			}
		}
	}
}
