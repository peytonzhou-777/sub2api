package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	apiKeyRateLimitKeyPrefix   = "apikey:ratelimit:"
	apiKeyRateLimitDuration    = 24 * time.Hour
	apiKeyAuthCachePrefix      = "apikey:auth:"
	authCacheInvalidateChannel = "auth:cache:invalidate"
	refundBillingLockTTL       = 7 * 24 * time.Hour
)

// apiKeyRateLimitKey generates the Redis key for API key creation rate limiting.
func apiKeyRateLimitKey(userID int64) string {
	return fmt.Sprintf("%s%d", apiKeyRateLimitKeyPrefix, userID)
}

func apiKeyAuthCacheKey(key string) string {
	return fmt.Sprintf("%s%s", apiKeyAuthCachePrefix, key)
}

type apiKeyCache struct {
	rdb *redis.Client
}

func refundBillingLockKey(userID int64) string {
	return fmt.Sprintf("refund:billing_lock:user:%d", userID)
}

var releaseRefundBillingLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

var acquireRefundBillingLockScript = redis.NewScript(`
local owner = redis.call("GET", KEYS[1])
if not owner then
  redis.call("SET", KEYS[1], ARGV[1], "PX", ARGV[2])
  return 1
end
if owner == ARGV[1] then
  redis.call("PEXPIRE", KEYS[1], ARGV[2])
  return 1
end
return 0
`)

// AcquireRefundBillingLock 建立或续期退款计费栅栏；TTL 只用于进程崩溃后的孤儿锁恢复。
func (c *apiKeyCache) AcquireRefundBillingLock(ctx context.Context, userID int64, refundID string) error {
	if userID <= 0 || strings.TrimSpace(refundID) == "" {
		return fmt.Errorf("refund billing lock requires user id and refund id")
	}
	acquired, err := acquireRefundBillingLockScript.Run(
		ctx,
		c.rdb,
		[]string{refundBillingLockKey(userID)},
		strings.TrimSpace(refundID),
		refundBillingLockTTL.Milliseconds(),
	).Int()
	if err != nil {
		return err
	}
	if acquired != 1 {
		return fmt.Errorf("refund billing lock is owned by another request")
	}
	return nil
}

// ReleaseRefundBillingLock 使用比较删除，避免旧流程误删新流程栅栏。
func (c *apiKeyCache) ReleaseRefundBillingLock(ctx context.Context, userID int64, refundID string) error {
	return releaseRefundBillingLockScript.Run(ctx, c.rdb, []string{refundBillingLockKey(userID)}, strings.TrimSpace(refundID)).Err()
}

// IsRefundBillingLocked 查询退款计费栅栏；Redis 错误由鉴权层按失败关闭处理。
func (c *apiKeyCache) IsRefundBillingLocked(ctx context.Context, userID int64) (bool, error) {
	count, err := c.rdb.Exists(ctx, refundBillingLockKey(userID)).Result()
	return count > 0, err
}

func NewAPIKeyCache(rdb *redis.Client) service.APIKeyCache {
	return &apiKeyCache{rdb: rdb}
}

func (c *apiKeyCache) GetCreateAttemptCount(ctx context.Context, userID int64) (int, error) {
	key := apiKeyRateLimitKey(userID)
	count, err := c.rdb.Get(ctx, key).Int()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return count, err
}

func (c *apiKeyCache) IncrementCreateAttemptCount(ctx context.Context, userID int64) error {
	key := apiKeyRateLimitKey(userID)
	pipe := c.rdb.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, apiKeyRateLimitDuration)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *apiKeyCache) DeleteCreateAttemptCount(ctx context.Context, userID int64) error {
	key := apiKeyRateLimitKey(userID)
	return c.rdb.Del(ctx, key).Err()
}

func (c *apiKeyCache) IncrementDailyUsage(ctx context.Context, apiKey string) error {
	return c.rdb.Incr(ctx, apiKey).Err()
}

func (c *apiKeyCache) SetDailyUsageExpiry(ctx context.Context, apiKey string, ttl time.Duration) error {
	return c.rdb.Expire(ctx, apiKey, ttl).Err()
}

func (c *apiKeyCache) GetAuthCache(ctx context.Context, key string) (*service.APIKeyAuthCacheEntry, error) {
	val, err := c.rdb.Get(ctx, apiKeyAuthCacheKey(key)).Bytes()
	if err != nil {
		return nil, err
	}
	var entry service.APIKeyAuthCacheEntry
	if err := json.Unmarshal(val, &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}

func (c *apiKeyCache) SetAuthCache(ctx context.Context, key string, entry *service.APIKeyAuthCacheEntry, ttl time.Duration) error {
	if entry == nil {
		return nil
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, apiKeyAuthCacheKey(key), payload, ttl).Err()
}

func (c *apiKeyCache) DeleteAuthCache(ctx context.Context, key string) error {
	return c.rdb.Del(ctx, apiKeyAuthCacheKey(key)).Err()
}

// PublishAuthCacheInvalidation publishes a cache invalidation message to all instances
func (c *apiKeyCache) PublishAuthCacheInvalidation(ctx context.Context, cacheKey string) error {
	return c.rdb.Publish(ctx, authCacheInvalidateChannel, cacheKey).Err()
}

// SubscribeAuthCacheInvalidation subscribes to cache invalidation messages
func (c *apiKeyCache) SubscribeAuthCacheInvalidation(ctx context.Context, handler func(cacheKey string)) error {
	pubsub := c.rdb.Subscribe(ctx, authCacheInvalidateChannel)

	// Verify subscription is working
	_, err := pubsub.Receive(ctx)
	if err != nil {
		_ = pubsub.Close()
		return fmt.Errorf("subscribe to auth cache invalidation: %w", err)
	}

	defer func() {
		if err := pubsub.Close(); err != nil {
			log.Printf("Warning: failed to close auth cache invalidation pubsub: %v", err)
		}
	}()
	service.NotifyAuthCacheSubscriptionReady(ctx)

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return errors.New("auth cache invalidation pubsub channel closed")
			}
			if msg != nil {
				handler(msg.Payload)
			}
		}
	}
}
