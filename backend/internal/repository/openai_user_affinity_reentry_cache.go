package repository

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const openAIUserAffinityReentryTTL = 3 * time.Minute

var initializeOpenAIUserAffinityReentryScript = redis.NewScript(`
	local state = KEYS[1]
	local queue = KEYS[2]
	local meta = KEYS[3]
	local seq = KEYS[4]
	local current = redis.call('HGET', state, 'batch')
	if current and current ~= ARGV[1] then
		if ARGV[7] ~= '1' then return -1 end
		redis.call('DEL', state, queue, meta, seq)
	end
	redis.call('HSET', state,
		'batch', ARGV[1], 'phase', ARGV[8], 'leader_version', ARGV[2],
		'leader_lease_ms', ARGV[3], 'jitter_min_ms', ARGV[4], 'jitter_max_ms', ARGV[5],
		'next_release_ms', ARGV[9], 'inflight', '', 'inflight_until_ms', 0)
	redis.call('PEXPIRE', state, ARGV[6])
	redis.call('PEXPIRE', queue, ARGV[6])
	redis.call('PEXPIRE', meta, ARGV[6])
	redis.call('PEXPIRE', seq, ARGV[6])
	return 1
`)

var enqueueOpenAIUserAffinityFollowerScript = redis.NewScript(`
	if redis.call('HGET', KEYS[1], 'batch') ~= ARGV[1] then
		return -1
	end
	if redis.call('ZSCORE', KEYS[2], ARGV[2]) then
		return 0
	end
	if redis.call('ZCARD', KEYS[2]) >= tonumber(ARGV[6]) then
		return -2
	end
	local seq = redis.call('INCR', KEYS[4])
	redis.call('ZADD', KEYS[2], seq, ARGV[2])
	redis.call('HSET', KEYS[3], ARGV[2], ARGV[3] .. ':' .. ARGV[4])
	redis.call('PEXPIRE', KEYS[1], ARGV[5])
	redis.call('PEXPIRE', KEYS[2], ARGV[5])
	redis.call('PEXPIRE', KEYS[3], ARGV[5])
	redis.call('PEXPIRE', KEYS[4], ARGV[5])
	return seq
`)

var pollOpenAIUserAffinityFollowerScript = redis.NewScript(`
	if redis.call('HGET', KEYS[1], 'batch') ~= ARGV[1] then
		return {-2, 0}
	end
	while true do
		local head = redis.call('ZRANGE', KEYS[2], 0, 0)[1]
		if not head then break end
		local raw = redis.call('HGET', KEYS[3], head)
		if not raw then
			redis.call('ZREM', KEYS[2], head)
		else
			local split = string.find(raw, ':')
			local deadline = tonumber(split and string.sub(raw, 1, split - 1) or raw)
			if deadline <= tonumber(ARGV[3]) then
				redis.call('ZREM', KEYS[2], head)
				redis.call('HDEL', KEYS[3], head)
			else break end
		end
	end
	local rank = redis.call('ZRANK', KEYS[2], ARGV[2])
	if not rank then return {-3, 0} end
	if rank ~= 0 then return {0, tonumber(redis.call('HGET', KEYS[1], 'leader_version') or '0')} end
	local phase = redis.call('HGET', KEYS[1], 'phase')
	local version = tonumber(redis.call('HGET', KEYS[1], 'leader_version') or '0')
	if phase == 'leader_pending' then
		local lease = tonumber(redis.call('HGET', KEYS[1], 'leader_lease_ms') or '0')
		if lease <= tonumber(ARGV[3]) then return {2, version} end
		return {0, version}
	end
	if phase ~= 'stagger_releasing' then return {0, version} end
	local next_at = tonumber(redis.call('HGET', KEYS[1], 'next_release_ms') or '0')
	local inflight_until = tonumber(redis.call('HGET', KEYS[1], 'inflight_until_ms') or '0')
	if next_at > tonumber(ARGV[3]) or inflight_until > tonumber(ARGV[3]) then return {0, version} end
	redis.call('ZREM', KEYS[2], ARGV[2])
	redis.call('HDEL', KEYS[3], ARGV[2])
	redis.call('HSET', KEYS[1], 'inflight', ARGV[2], 'inflight_until_ms', tonumber(ARGV[3]) + 30000)
	return {1, version}
`)

var activateOpenAIUserAffinityFollowersScript = redis.NewScript(`
	if redis.call('HGET', KEYS[1], 'batch') ~= ARGV[1] then return -1 end
	redis.call('HSET', KEYS[1], 'phase', 'stagger_releasing', 'leader_lease_ms', 0,
		'next_release_ms', ARGV[2], 'inflight', '', 'inflight_until_ms', 0)
	redis.call('PEXPIRE', KEYS[1], ARGV[3])
	return redis.call('ZCARD', KEYS[2])
`)

var failOpenAIUserAffinityLeaderScript = redis.NewScript(`
	if redis.call('HGET', KEYS[1], 'batch') ~= ARGV[1] then return 0 end
	if tonumber(redis.call('HGET', KEYS[1], 'leader_version') or '0') ~= tonumber(ARGV[2]) then return 0 end
	redis.call('HSET', KEYS[1], 'phase', 'leader_pending', 'leader_lease_ms', 0)
	return 1
`)

var acknowledgeOpenAIUserAffinityFollowerScript = redis.NewScript(`
	if redis.call('HGET', KEYS[1], 'batch') ~= ARGV[1] then return -1 end
	if redis.call('HGET', KEYS[1], 'inflight') ~= ARGV[2] then return -2 end
	redis.call('HSET', KEYS[1], 'inflight', '', 'inflight_until_ms', 0, 'next_release_ms', ARGV[3])
	return redis.call('ZCARD', KEYS[2])
`)

func openAIUserAffinityReentryKeys(accountID, userID int64) []string {
	tag := fmt.Sprintf("{%d:%d}", accountID, userID)
	return []string{
		"resident_reentry_state:" + tag,
		"resident_reentry_queue:" + tag,
		"resident_reentry_meta:" + tag,
		"resident_reentry_queue_seq:" + tag,
	}
}

// InitializeOpenAIUserAffinityReentry 发布数据库已选出的 leader 版本和租约。
func (c *gatewayCache) InitializeOpenAIUserAffinityReentry(ctx context.Context, admission service.OpenAIUserAffinityReentryAdmission) error {
	if c == nil || c.rdb == nil {
		return errors.New("openai user affinity reentry cache unavailable")
	}
	lease := admission.LeaderLeaseUntil
	if lease.IsZero() {
		lease = time.Now().Add(30 * time.Second)
	}
	if !admission.Deadline.IsZero() && admission.Deadline.Before(lease) {
		lease = admission.Deadline
	}
	overwrite := 0
	if admission.Leader {
		overwrite = 1
	}
	phase := admission.ReentryState
	if phase != "stagger_releasing" {
		phase = "leader_pending"
	}
	nextReleaseAt := int64(0)
	if phase == "stagger_releasing" {
		lease = time.UnixMilli(0)
		nextReleaseAt = time.Now().Add(openAIUserAffinityFollowerJitter(admission)).UnixMilli()
	}
	result, err := initializeOpenAIUserAffinityReentryScript.Run(ctx, c.rdb,
		openAIUserAffinityReentryKeys(admission.AccountID, admission.UserID), admission.BatchToken,
		admission.LeaderVersion, lease.UnixMilli(), admission.JitterMinMS, admission.JitterMaxMS,
		openAIUserAffinityReentryTTL.Milliseconds(), overwrite, phase, nextReleaseAt).Int64()
	if err != nil {
		return err
	}
	if result < 0 {
		return service.ErrOpenAIUserAffinityReentryBatchNotReady
	}
	return nil
}

func (c *gatewayCache) EnqueueOpenAIUserAffinityFollower(ctx context.Context, admission service.OpenAIUserAffinityReentryAdmission) error {
	deadline := admission.Deadline
	if deadline.IsZero() {
		deadline = time.Now().Add(2 * time.Minute)
	}
	maxFollowers := admission.MaxFollowers
	if maxFollowers <= 0 {
		maxFollowers = 100
	}
	result, err := enqueueOpenAIUserAffinityFollowerScript.Run(ctx, c.rdb,
		openAIUserAffinityReentryKeys(admission.AccountID, admission.UserID), admission.BatchToken,
		admission.WaiterToken, deadline.UnixMilli(), admission.Generation,
		openAIUserAffinityReentryTTL.Milliseconds(), maxFollowers).Int64()
	if err != nil {
		return err
	}
	if result == -1 {
		return service.ErrOpenAIUserAffinityReentryBatchNotReady
	}
	if result == -2 {
		return service.ErrOpenAIUserAffinityReentryQueueFull
	}
	return nil
}

func (c *gatewayCache) PollOpenAIUserAffinityFollower(ctx context.Context, admission service.OpenAIUserAffinityReentryAdmission, now time.Time) (service.OpenAIUserAffinityFollowerPoll, error) {
	values, err := pollOpenAIUserAffinityFollowerScript.Run(ctx, c.rdb,
		openAIUserAffinityReentryKeys(admission.AccountID, admission.UserID), admission.BatchToken,
		admission.WaiterToken, now.UnixMilli(), admission.Generation).Slice()
	if err != nil {
		return service.OpenAIUserAffinityFollowerPoll{}, err
	}
	if len(values) != 2 {
		return service.OpenAIUserAffinityFollowerPoll{}, errors.New("invalid openai user affinity follower poll result")
	}
	code, err := redisResultInt64(values[0])
	if err != nil {
		return service.OpenAIUserAffinityFollowerPoll{}, err
	}
	version, err := redisResultInt64(values[1])
	if err != nil {
		return service.OpenAIUserAffinityFollowerPoll{}, err
	}
	if code < -1 {
		return service.OpenAIUserAffinityFollowerPoll{}, errors.New("openai user affinity follower is stale")
	}
	return service.OpenAIUserAffinityFollowerPoll{Released: code == 1, MayTakeover: code == 2, ExpectedLeaderVersion: version}, nil
}

func (c *gatewayCache) ActivateOpenAIUserAffinityFollowers(ctx context.Context, admission service.OpenAIUserAffinityReentryAdmission, now time.Time) (bool, error) {
	next := now.Add(openAIUserAffinityFollowerJitter(admission))
	count, err := activateOpenAIUserAffinityFollowersScript.Run(ctx, c.rdb,
		openAIUserAffinityReentryKeys(admission.AccountID, admission.UserID), admission.BatchToken,
		next.UnixMilli(), openAIUserAffinityReentryTTL.Milliseconds()).Int64()
	return count == 0, err
}

func (c *gatewayCache) MarkOpenAIUserAffinityLeaderFailed(ctx context.Context, admission service.OpenAIUserAffinityReentryAdmission) error {
	return failOpenAIUserAffinityLeaderScript.Run(ctx, c.rdb,
		openAIUserAffinityReentryKeys(admission.AccountID, admission.UserID), admission.BatchToken,
		admission.LeaderVersion).Err()
}

func (c *gatewayCache) AcknowledgeOpenAIUserAffinityFollower(ctx context.Context, admission service.OpenAIUserAffinityReentryAdmission, now time.Time) (bool, error) {
	next := now.Add(openAIUserAffinityFollowerJitter(admission))
	count, err := acknowledgeOpenAIUserAffinityFollowerScript.Run(ctx, c.rdb,
		openAIUserAffinityReentryKeys(admission.AccountID, admission.UserID), admission.BatchToken,
		admission.WaiterToken, next.UnixMilli()).Int64()
	return count == 0, err
}

func (c *gatewayCache) RemoveOpenAIUserAffinityFollower(ctx context.Context, admission service.OpenAIUserAffinityReentryAdmission) error {
	keys := openAIUserAffinityReentryKeys(admission.AccountID, admission.UserID)
	pipe := c.rdb.TxPipeline()
	pipe.ZRem(ctx, keys[1], admission.WaiterToken)
	pipe.HDel(ctx, keys[2], admission.WaiterToken)
	_, err := pipe.Exec(ctx)
	return err
}

func openAIUserAffinityFollowerJitter(admission service.OpenAIUserAffinityReentryAdmission) time.Duration {
	minMS, maxMS := admission.JitterMinMS, admission.JitterMaxMS
	if minMS < 0 {
		minMS = 0
	}
	if maxMS < minMS {
		maxMS = minMS
	}
	if maxMS == minMS {
		return time.Duration(minMS) * time.Millisecond
	}
	return time.Duration(minMS+rand.Intn(maxMS-minMS+1)) * time.Millisecond
}

func redisResultInt64(value any) (int64, error) {
	switch current := value.(type) {
	case int64:
		return current, nil
	case string:
		return strconv.ParseInt(current, 10, 64)
	case []byte:
		return strconv.ParseInt(string(current), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected redis integer type %T", value)
	}
}

var _ service.OpenAIUserAffinityReentryQueue = (*gatewayCache)(nil)
