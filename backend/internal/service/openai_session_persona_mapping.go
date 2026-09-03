package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// SessionPersonaMappingScopeKey 返回动态 AccountPersona 的持久化 ID 映射作用域。
// 未携带稳定 AccountPersona 主键的旧固定槽绑定不再形成可复用作用域。
func SessionPersonaMappingScopeKey(binding SessionPersonaSlotBinding) string {
	binding = binding.NormalizeLifecycle()
	if binding.AccountID <= 0 || binding.AccountPersonaID <= 0 {
		return ""
	}
	seed := strings.Join([]string{
		"openai-account-persona-map:v1",
		formatInt64(binding.AccountID),
		formatInt64(binding.AccountPersonaID),
		string(binding.PersonaID),
		formatInt64(binding.SessionEpoch),
		formatInt64(binding.SlotGeneration),
		strings.TrimSpace(binding.PersonaVersion),
		strings.TrimSpace(binding.CredentialChainID),
		strings.TrimSpace(binding.ClientThreadID),
	}, "|")
	digest := sha256.Sum256([]byte(seed))
	return "pm_" + hex.EncodeToString(digest[:16])
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}
