package service

import "context"

// effectiveOpenAIAccountAdmissionCapacity 返回调度实际使用的账号总容量。
func (s *OpenAIGatewayService) effectiveOpenAIAccountAdmissionCapacity(ctx context.Context, account *Account) int {
	cfg := DefaultOpenAIAccountAdmissionConfig()
	if s != nil && s.settingService != nil {
		if current, err := s.settingService.GetOpenAIAccountAdmissionConfig(ctx); err == nil {
			cfg = current
		}
	}
	if account != nil && account.IsOpenAIOAuth() {
		if s.accountPersonaRepo == nil {
			return 0
		}
		personas, err := s.accountPersonaRepo.ListAccountPersonas(ctx, account.ID)
		if err != nil {
			return 0
		}
		return EffectiveOpenAIAccountPersonaCapacity(account, personas, cfg)
	}
	return EffectiveOpenAIAccountAdmissionCapacity(account, cfg)
}

// EffectiveOpenAIAccountAdmissionCapacity exposes the same database-backed
// capacity snapshot used by scheduling to protocol admission handlers.
func (s *OpenAIGatewayService) EffectiveOpenAIAccountAdmissionCapacity(ctx context.Context, account *Account) int {
	return s.effectiveOpenAIAccountAdmissionCapacity(ctx, account)
}

// effectiveOpenAIAccountLoadFactor 保留显式调度权重；未配置时以 Persona
// 有效容量作为负载分母，避免继续使用旧账号并发值。
func (s *OpenAIGatewayService) effectiveOpenAIAccountLoadFactor(ctx context.Context, account *Account) int {
	if account != nil && account.LoadFactor != nil && *account.LoadFactor > 0 {
		return *account.LoadFactor
	}
	return s.effectiveOpenAIAccountAdmissionCapacity(ctx, account)
}
