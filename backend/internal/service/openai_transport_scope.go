package service

import (
	"context"
	"strings"
)

// OpenAITransportScope 是 OpenAI OAuth 专用 Transport 的不可变作用域快照。
//
// 该快照只从已经完成的 Persona/slot 绑定派生，不包含 token 内容。Transport
// 层必须把它视为 Account × Persona × Slot × Session Epoch × Credential Chain
// 的最小隔离边界，不能退化为账号级或代理级复用。
type OpenAITransportScope struct {
	AccountID         int64
	Persona           SessionPersonaID
	PersonaVersion    string
	SlotID            int
	SessionEpoch      int64
	SlotGeneration    int64
	SlotSetGeneration int64
	CredentialChainID string
	InstallationID    string
}

// ReadyForCPA 表示当前请求具备进入 CPA Transport 的完整身份快照。
// 缺少任一关键代际字段时返回 false，由调用方保留旧兼容 Transport。
func (s OpenAITransportScope) ReadyForCPA(accountID int64) bool {
	return s.AccountID > 0 && s.AccountID == accountID &&
		strings.TrimSpace(string(s.Persona)) != "" &&
		s.SlotID >= 0 &&
		s.SessionEpoch > 0 &&
		s.SlotGeneration > 0 &&
		s.SlotSetGeneration > 0 &&
		strings.TrimSpace(s.CredentialChainID) != ""
}

// OpenAITransportScopeFromContext 从请求上下文读取 Persona/slot 绑定。
// 只有完整 v3 绑定才会返回 true；旧 v1/v2 或不完整兼容请求继续走旧路径。
func OpenAITransportScopeFromContext(ctx context.Context, accountID int64) (OpenAITransportScope, bool) {
	binding, ok := SessionPersonaBindingFromContext(ctx)
	if !ok {
		return OpenAITransportScope{}, false
	}
	if binding.EffectiveMappingVersion() < SessionPersonaScopeVersionV3 {
		return OpenAITransportScope{}, false
	}
	scope := OpenAITransportScope{
		AccountID:         binding.AccountID,
		Persona:           binding.PersonaID,
		PersonaVersion:    strings.TrimSpace(binding.PersonaVersion),
		SlotID:            binding.SlotID,
		SessionEpoch:      binding.SessionEpoch,
		SlotGeneration:    binding.SlotGeneration,
		SlotSetGeneration: binding.SlotSetGeneration,
		CredentialChainID: strings.TrimSpace(binding.CredentialChainID),
		InstallationID:    strings.TrimSpace(binding.InstallationID),
	}
	if !scope.ReadyForCPA(accountID) {
		return OpenAITransportScope{}, false
	}
	return scope, true
}
