package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const openAIPersonaActiveSessionPrefix = "openai:persona_active_sessions:"

func openAIPersonaActiveSessionKeys(accountID int64, persona, clientSessionHash string) (string, string, string) {
	scope := fmt.Sprintf("{%d:%s}", accountID, strings.ToLower(strings.TrimSpace(persona)))
	base := openAIPersonaActiveSessionPrefix + scope
	return base + ":expires", base + ":state", base + ":pending:" + strings.ToLower(strings.TrimSpace(clientSessionHash))
}

var reserveOpenAIPersonaActiveSessionScript = redis.NewScript(`
local expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if #expired > 0 then
  redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
  redis.call('HDEL', KEYS[2], unpack(expired))
end
local current = redis.call('ZSCORE', KEYS[1], ARGV[4])
if current then
  local state = redis.call('HGET', KEYS[2], ARGV[4])
  if state == 'a' or state == false then
    if state == false then redis.call('HSET', KEYS[2], ARGV[4], 'a') end
    if tonumber(current) < tonumber(ARGV[2]) then
      redis.call('ZADD', KEYS[1], ARGV[2], ARGV[4])
    end
    redis.call('PEXPIRE', KEYS[1], ARGV[6])
    redis.call('PEXPIRE', KEYS[2], ARGV[6])
    return 2
  end
  redis.call('SADD', KEYS[3], ARGV[5])
  redis.call('PEXPIRE', KEYS[3], ARGV[7])
  if tonumber(current) < tonumber(ARGV[2]) then
    redis.call('ZADD', KEYS[1], ARGV[2], ARGV[4])
  end
  redis.call('PEXPIRE', KEYS[1], ARGV[6])
  redis.call('PEXPIRE', KEYS[2], ARGV[6])
  return 3
end
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[3]) then return 0 end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[4])
redis.call('HSET', KEYS[2], ARGV[4], 'p')
redis.call('SADD', KEYS[3], ARGV[5])
redis.call('PEXPIRE', KEYS[3], ARGV[7])
redis.call('PEXPIRE', KEYS[1], ARGV[6])
redis.call('PEXPIRE', KEYS[2], ARGV[6])
return 1
`)

var commitOpenAIPersonaActiveSessionScript = redis.NewScript(`
local expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if #expired > 0 then
  redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
  redis.call('HDEL', KEYS[2], unpack(expired))
end
if redis.call('ZSCORE', KEYS[1], ARGV[3]) == false then return 0 end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[3])
redis.call('HSET', KEYS[2], ARGV[3], 'a')
redis.call('DEL', KEYS[3])
redis.call('PEXPIRE', KEYS[1], ARGV[4])
redis.call('PEXPIRE', KEYS[2], ARGV[4])
return 1
`)

var releaseOpenAIPersonaActiveSessionScript = redis.NewScript(`
if redis.call('HGET', KEYS[2], ARGV[1]) ~= 'p' then return 0 end
redis.call('SREM', KEYS[3], ARGV[2])
if redis.call('SCARD', KEYS[3]) > 0 then return 1 end
redis.call('ZREM', KEYS[1], ARGV[1])
redis.call('HDEL', KEYS[2], ARGV[1])
redis.call('DEL', KEYS[3])
return 2
`)

func (c *gatewayCache) ReserveOpenAIPersonaActiveSession(ctx context.Context, accountID int64, persona, clientSessionHash, reservationID string, maxSessions int, pendingTTL time.Duration) (service.OpenAIPersonaActiveSessionReservationState, error) {
	if c == nil || c.rdb == nil || accountID <= 0 || strings.TrimSpace(persona) == "" || len(strings.TrimSpace(clientSessionHash)) != 64 || strings.TrimSpace(reservationID) == "" || maxSessions <= 0 || pendingTTL <= 0 {
		return service.OpenAIPersonaActiveSessionRejected, errors.New("invalid OpenAI Persona active Session reservation")
	}
	expiresKey, stateKey, pendingKey := openAIPersonaActiveSessionKeys(accountID, persona, clientSessionHash)
	now := time.Now().UTC()
	keyTTL := pendingTTL + time.Hour
	result, err := reserveOpenAIPersonaActiveSessionScript.Run(ctx, c.rdb, []string{expiresKey, stateKey, pendingKey},
		now.UnixMilli(), now.Add(pendingTTL).UnixMilli(), maxSessions,
		strings.ToLower(strings.TrimSpace(clientSessionHash)), strings.TrimSpace(reservationID),
		keyTTL.Milliseconds(), pendingTTL.Milliseconds()).Int()
	return service.OpenAIPersonaActiveSessionReservationState(result), err
}

func (c *gatewayCache) CommitOpenAIPersonaActiveSession(ctx context.Context, accountID int64, persona, clientSessionHash string, activeTTL time.Duration) (bool, error) {
	if c == nil || c.rdb == nil || accountID <= 0 || strings.TrimSpace(persona) == "" || len(strings.TrimSpace(clientSessionHash)) != 64 || activeTTL <= 0 {
		return false, errors.New("invalid OpenAI Persona active Session commit")
	}
	expiresKey, stateKey, pendingKey := openAIPersonaActiveSessionKeys(accountID, persona, clientSessionHash)
	now := time.Now().UTC()
	result, err := commitOpenAIPersonaActiveSessionScript.Run(ctx, c.rdb, []string{expiresKey, stateKey, pendingKey},
		now.UnixMilli(), now.Add(activeTTL).UnixMilli(), strings.ToLower(strings.TrimSpace(clientSessionHash)),
		(activeTTL + time.Hour).Milliseconds()).Int()
	return result == 1, err
}

func (c *gatewayCache) ReleaseOpenAIPersonaActiveSessionReservation(ctx context.Context, accountID int64, persona, clientSessionHash, reservationID string) error {
	if c == nil || c.rdb == nil || accountID <= 0 || strings.TrimSpace(persona) == "" || len(strings.TrimSpace(clientSessionHash)) != 64 || strings.TrimSpace(reservationID) == "" {
		return errors.New("invalid OpenAI Persona active Session release")
	}
	expiresKey, stateKey, pendingKey := openAIPersonaActiveSessionKeys(accountID, persona, clientSessionHash)
	return releaseOpenAIPersonaActiveSessionScript.Run(ctx, c.rdb, []string{expiresKey, stateKey, pendingKey},
		strings.ToLower(strings.TrimSpace(clientSessionHash)), strings.TrimSpace(reservationID)).Err()
}

var _ service.OpenAIPersonaActiveSessionCache = (*gatewayCache)(nil)
