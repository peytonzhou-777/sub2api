package service

import (
	"fmt"
	"strconv"
	"strings"
)

func OpenAITokenCacheKeyForAccountPersona(accountPersonaID int64, credentialChainID string) string {
	return fmt.Sprintf("openai:account-persona:v4:%d:%s", accountPersonaID, strings.TrimSpace(credentialChainID))
}

// OpenAITokenCacheKey 生成 OpenAI OAuth 账号的缓存键
// 格式: "openai:account:{account_id}"
func OpenAITokenCacheKey(account *Account) string {
	if account == nil {
		return "openai:account:0"
	}
	return "openai:account:" + strconv.FormatInt(account.ID, 10)
}

// ClaudeTokenCacheKey 生成 Claude (Anthropic) OAuth 账号的缓存键
// 格式: "claude:account:{account_id}"
func ClaudeTokenCacheKey(account *Account) string {
	return "claude:account:" + strconv.FormatInt(account.ID, 10)
}
