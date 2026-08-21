package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

type openAIAccountAdmissionQueue struct {
	rdb *redis.Client
}

// NewOpenAIAccountAdmissionQueue 创建按账号 Redis hash-tag 隔离的跨实例准入队列。
func NewOpenAIAccountAdmissionQueue(rdb *redis.Client) service.OpenAIAccountAdmissionQueue {
	return &openAIAccountAdmissionQueue{rdb: rdb}
}

var enqueueOpenAIAccountAdmissionScript = redis.NewScript(`
	local t = redis.call('TIME')
	local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
	local function remove(ticket)
		redis.call('ZREM', KEYS[1], ticket)
		redis.call('ZREM', KEYS[2], ticket)
		redis.call('ZREM', KEYS[3], ticket)
		redis.call('HDEL', KEYS[4], ticket)
		redis.call('HDEL', KEYS[5], ticket)
		redis.call('HDEL', KEYS[6], ticket)
		redis.call('HDEL', KEYS[9], ticket)
		redis.call('HDEL', KEYS[10], ticket)
	end
	local expired = redis.call('ZRANGEBYSCORE', KEYS[3], '-inf', now)
	for _, ticket in ipairs(expired) do remove(ticket) end
	local depth = redis.call('ZCARD', KEYS[1]) + redis.call('ZCARD', KEYS[2])
	if redis.call('ZSCORE', KEYS[3], ARGV[1]) then return 0 end
	if depth >= tonumber(ARGV[6]) then return -1 end
	local seq = redis.call('INCR', KEYS[7])
	local lane = KEYS[1]
	if ARGV[2] == 'background' then lane = KEYS[2] end
	redis.call('ZADD', lane, seq, ARGV[1])
	redis.call('ZADD', KEYS[3], now + tonumber(ARGV[3]), ARGV[1])
	redis.call('HSET', KEYS[4], ARGV[1], now)
	redis.call('HSET', KEYS[5], ARGV[1], ARGV[4])
	redis.call('HSET', KEYS[6], ARGV[1], ARGV[2])
	redis.call('HSET', KEYS[9], ARGV[1], ARGV[7])
	redis.call('HSET', KEYS[10], ARGV[1], ARGV[8])
	for i = 1, #KEYS do redis.call('PEXPIRE', KEYS[i], ARGV[5]) end
	return 1
`)

var pollOpenAIAccountAdmissionScript = redis.NewScript(`
	local t = redis.call('TIME')
	local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
	local function remove(ticket)
		redis.call('ZREM', KEYS[1], ticket)
		redis.call('ZREM', KEYS[2], ticket)
		redis.call('ZREM', KEYS[3], ticket)
		redis.call('HDEL', KEYS[4], ticket)
		redis.call('HDEL', KEYS[5], ticket)
		redis.call('HDEL', KEYS[6], ticket)
		redis.call('HDEL', KEYS[9], ticket)
		redis.call('HDEL', KEYS[10], ticket)
	end
	local expired = redis.call('ZRANGEBYSCORE', KEYS[3], '-inf', now)
	for _, ticket in ipairs(expired) do remove(ticket) end
	local interactive = redis.call('ZRANGE', KEYS[1], 0, 0)[1]
	local background = redis.call('ZRANGE', KEYS[2], 0, 0)[1]
	if not redis.call('ZSCORE', KEYS[3], ARGV[1]) then return {-1, 0} end
	local chosen = interactive
	if not interactive then
		chosen = background
	elseif background then
		local enqueued = tonumber(redis.call('HGET', KEYS[4], background) or tostring(now))
		local burst = tonumber(redis.call('HGET', KEYS[8], 'interactive_burst') or '0')
		local aging = tonumber(redis.call('HGET', KEYS[10], background) or '0')
		local burst_limit = tonumber(redis.call('HGET', KEYS[9], interactive) or '1')
		if enqueued + aging <= now or burst >= burst_limit then chosen = background end
	end
	if chosen ~= ARGV[1] then return {0, 0} end
	local rpm_tat = 0
	local tpm_tat = 0
	if tonumber(ARGV[2]) > 0 then rpm_tat = tonumber(redis.call('HGET', KEYS[8], 'rpm_tat_ms') or '0') end
	if tonumber(ARGV[3]) > 0 then tpm_tat = tonumber(redis.call('HGET', KEYS[8], 'tpm_tat_ms') or '0') end
	local ready = math.max(now, rpm_tat, tpm_tat)
	if tonumber(ARGV[3]) > 0 and tonumber(ARGV[4]) > tonumber(ARGV[3]) then return {2, 60000} end
	if ready > now then return {1, ready - now} end
	return {1, 0}
`)

var grantOpenAIAccountAdmissionScript = redis.NewScript(`
	local t = redis.call('TIME')
	local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
	local function remove(ticket)
		redis.call('ZREM', KEYS[1], ticket)
		redis.call('ZREM', KEYS[2], ticket)
		redis.call('ZREM', KEYS[3], ticket)
		redis.call('HDEL', KEYS[4], ticket)
		redis.call('HDEL', KEYS[5], ticket)
		redis.call('HDEL', KEYS[6], ticket)
		redis.call('HDEL', KEYS[9], ticket)
		redis.call('HDEL', KEYS[10], ticket)
	end
	local expired = redis.call('ZRANGEBYSCORE', KEYS[3], '-inf', now)
	for _, ticket in ipairs(expired) do remove(ticket) end
	local interactive = redis.call('ZRANGE', KEYS[1], 0, 0)[1]
	local background = redis.call('ZRANGE', KEYS[2], 0, 0)[1]
	if not redis.call('ZSCORE', KEYS[3], ARGV[1]) then return {-1, 0} end
	local chosen = interactive
	if not interactive then
		chosen = background
	elseif background then
		local enqueued = tonumber(redis.call('HGET', KEYS[4], background) or tostring(now))
		local burst = tonumber(redis.call('HGET', KEYS[8], 'interactive_burst') or '0')
		local aging = tonumber(redis.call('HGET', KEYS[10], background) or '0')
		local burst_limit = tonumber(redis.call('HGET', KEYS[9], interactive) or '1')
		if enqueued + aging <= now or burst >= burst_limit then chosen = background end
	end
	if chosen ~= ARGV[1] then return {0, 0} end
	local tokens = tonumber(redis.call('HGET', KEYS[5], ARGV[1]) or '1')
	if tonumber(ARGV[3]) > 0 and tokens > tonumber(ARGV[3]) then return {0, 60000} end
	local rpm_tat = 0
	local tpm_tat = 0
	if tonumber(ARGV[2]) > 0 then rpm_tat = tonumber(redis.call('HGET', KEYS[8], 'rpm_tat_ms') or '0') end
	if tonumber(ARGV[3]) > 0 then tpm_tat = tonumber(redis.call('HGET', KEYS[8], 'tpm_tat_ms') or '0') end
	local ready = math.max(now, rpm_tat, tpm_tat)
	if ready > now then return {0, ready - now} end
	local dispatch = now + tonumber(ARGV[4])
	if tonumber(ARGV[2]) > 0 then
		redis.call('HSET', KEYS[8], 'rpm_tat_ms', dispatch + (60000 / tonumber(ARGV[2])))
	end
	if tonumber(ARGV[3]) > 0 then
		redis.call('HSET', KEYS[8], 'tpm_tat_ms', dispatch + (60000 * tokens / tonumber(ARGV[3])))
	end
	local class = redis.call('HGET', KEYS[6], ARGV[1])
	if class == 'background' then
		redis.call('HSET', KEYS[8], 'interactive_burst', 0)
	else
		redis.call('HINCRBY', KEYS[8], 'interactive_burst', 1)
	end
	remove(ARGV[1])
	redis.call('PEXPIRE', KEYS[8], ARGV[5])
	return {1, tonumber(ARGV[4])}
`)

func openAIAccountAdmissionKeys(accountID int64) []string {
	tag := fmt.Sprintf("{%d}", accountID)
	return []string{
		"openai:admission:" + tag + ":interactive",
		"openai:admission:" + tag + ":background",
		"openai:admission:" + tag + ":deadline",
		"openai:admission:" + tag + ":enqueued",
		"openai:admission:" + tag + ":tokens",
		"openai:admission:" + tag + ":class",
		"openai:admission:" + tag + ":seq",
		"openai:admission:" + tag + ":state",
		"openai:admission:" + tag + ":burst_limit",
		"openai:admission:" + tag + ":aging_ms",
	}
}

func (q *openAIAccountAdmissionQueue) Enqueue(ctx context.Context, ticket service.OpenAIAccountAdmissionTicket, cfg service.OpenAIAccountAdmissionConfig) error {
	if q == nil || q.rdb == nil {
		return errors.New("openai account admission redis unavailable")
	}
	ttl := time.Until(ticket.Deadline) + time.Minute
	if ttl < time.Minute {
		ttl = time.Minute
	}
	result, err := enqueueOpenAIAccountAdmissionScript.Run(ctx, q.rdb, openAIAccountAdmissionKeys(ticket.AccountID),
		ticket.ID, string(ticket.Class), time.Duration(cfg.MaxWaitSeconds)*time.Second/time.Millisecond,
		max(ticket.EstimatedTokens, 1), ttl.Milliseconds(), cfg.MaxQueueDepthPerAccount, cfg.InteractiveBurst,
		time.Duration(cfg.BackgroundAgingSeconds)*time.Second/time.Millisecond).Int64()
	if err != nil {
		return err
	}
	if result < 0 {
		return service.ErrOpenAIAdmissionQueueFull
	}
	return nil
}

func (q *openAIAccountAdmissionQueue) Poll(ctx context.Context, ticket service.OpenAIAccountAdmissionTicket, cfg service.OpenAIAccountAdmissionConfig) (service.OpenAIAccountAdmissionPoll, error) {
	result, err := pollOpenAIAccountAdmissionScript.Run(ctx, q.rdb, openAIAccountAdmissionKeys(ticket.AccountID),
		ticket.ID, cfg.RequestsPerMinute, cfg.TokensPerMinute, max(ticket.EstimatedTokens, 1)).Result()
	code, delay, err := parseOpenAIAdmissionResult(result, err)
	if err != nil {
		return service.OpenAIAccountAdmissionPoll{}, err
	}
	if code < 0 {
		return service.OpenAIAccountAdmissionPoll{}, service.ErrOpenAIAdmissionTicketGone
	}
	return service.OpenAIAccountAdmissionPoll{Selected: code == 1, Delay: time.Duration(delay) * time.Millisecond}, nil
}

func (q *openAIAccountAdmissionQueue) Grant(ctx context.Context, ticket service.OpenAIAccountAdmissionTicket, cfg service.OpenAIAccountAdmissionConfig, jitter time.Duration) (service.OpenAIAccountAdmissionGrant, error) {
	ttl := time.Duration(cfg.MaxWaitSeconds+60) * time.Second
	result, err := grantOpenAIAccountAdmissionScript.Run(ctx, q.rdb, openAIAccountAdmissionKeys(ticket.AccountID),
		ticket.ID, cfg.RequestsPerMinute, cfg.TokensPerMinute, jitter.Milliseconds(), ttl.Milliseconds()).Result()
	code, delay, err := parseOpenAIAdmissionResult(result, err)
	if err != nil {
		return service.OpenAIAccountAdmissionGrant{}, err
	}
	if code < 0 {
		return service.OpenAIAccountAdmissionGrant{}, service.ErrOpenAIAdmissionTicketGone
	}
	return service.OpenAIAccountAdmissionGrant{Granted: code == 1, Delay: time.Duration(delay) * time.Millisecond}, nil
}

func (q *openAIAccountAdmissionQueue) Remove(ctx context.Context, ticket service.OpenAIAccountAdmissionTicket) error {
	if q == nil || q.rdb == nil || ticket.ID == "" {
		return nil
	}
	keys := openAIAccountAdmissionKeys(ticket.AccountID)
	pipe := q.rdb.TxPipeline()
	pipe.ZRem(ctx, keys[0], ticket.ID)
	pipe.ZRem(ctx, keys[1], ticket.ID)
	pipe.ZRem(ctx, keys[2], ticket.ID)
	pipe.HDel(ctx, keys[3], ticket.ID)
	pipe.HDel(ctx, keys[4], ticket.ID)
	pipe.HDel(ctx, keys[5], ticket.ID)
	pipe.HDel(ctx, keys[8], ticket.ID)
	pipe.HDel(ctx, keys[9], ticket.ID)
	_, err := pipe.Exec(ctx)
	return err
}

func parseOpenAIAdmissionResult(result any, sourceErr error) (int64, int64, error) {
	if sourceErr != nil {
		return 0, 0, sourceErr
	}
	values, ok := result.([]any)
	if !ok || len(values) < 2 {
		return 0, 0, fmt.Errorf("unexpected openai admission redis result %T", result)
	}
	code, err := redisAdmissionInt64(values[0])
	if err != nil {
		return 0, 0, err
	}
	delay, err := redisAdmissionInt64(values[1])
	return code, delay, err
}

func redisAdmissionInt64(value any) (int64, error) {
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

var _ service.OpenAIAccountAdmissionQueue = (*openAIAccountAdmissionQueue)(nil)
