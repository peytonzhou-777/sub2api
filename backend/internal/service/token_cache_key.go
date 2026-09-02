package service

import (
	"strconv"
	"strings"
)

// OpenAITokenCacheKey 生成 OpenAI OAuth 账号的缓存键
// 格式: "openai:account:{account_id}"
func OpenAITokenCacheKey(account *Account) string {
	if account == nil {
		return "openai:account:0"
	}
	return "openai:account:" + strconv.FormatInt(account.ID, 10)
}

// OpenAITokenCacheKeyForPersona isolates OAuth token/refresh locks by
// Account × Persona × credential chain. The legacy key remains unchanged when
// no Persona or chain is supplied, so historical callers and cache entries are
// still readable during the staged migration.
func OpenAITokenCacheKeyForPersona(account *Account, persona SessionPersonaID, credentialChainID string) string {
	base := OpenAITokenCacheKey(account)
	persona = normalizeSessionPersonaID(persona)
	credentialChainID = sanitizeOpenAITokenCacheKeyPart(credentialChainID)
	if persona == "" && credentialChainID == "" {
		return base
	}
	if persona == "" {
		persona = "legacy"
	}
	if credentialChainID == "" {
		credentialChainID = "legacy"
	}
	return base + ":persona:" + sanitizeOpenAITokenCacheKeyPart(string(persona)) + ":chain:" + credentialChainID
}

// OpenAITokenCacheKeyForBinding is the typed convenience wrapper used by new
// Persona-aware token providers and refreshers.
func OpenAITokenCacheKeyForBinding(account *Account, binding SessionPersonaSlotBinding) string {
	return OpenAITokenCacheKeyForPersona(account, binding.PersonaID, binding.CredentialChainID)
}

func normalizeSessionPersonaID(persona SessionPersonaID) SessionPersonaID {
	if canonical, ok := ParseSessionPersonaID(string(persona)); ok {
		return canonical
	}
	return SessionPersonaID(strings.TrimSpace(string(persona)))
}

func sanitizeOpenAITokenCacheKeyPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	normalized := make([]byte, 0, len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			normalized = append(normalized, byte(r))
		default:
			normalized = append(normalized, '_')
		}
	}
	return strings.Trim(string(normalized), "_")
}

// ClaudeTokenCacheKey 生成 Claude (Anthropic) OAuth 账号的缓存键
// 格式: "claude:account:{account_id}"
func ClaudeTokenCacheKey(account *Account) string {
	return "claude:account:" + strconv.FormatInt(account.ID, 10)
}
