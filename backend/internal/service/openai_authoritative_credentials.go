package service

import (
	"context"
	"fmt"
)

// resolveAuthoritativeOpenAICredentialAccount 按选号账号 ID 从权威仓库加载凭据，
// 再将影子账号解析到母账号。调度快照只负责选号，不能作为密钥来源。
func (s *OpenAIGatewayService) resolveAuthoritativeOpenAICredentialAccount(ctx context.Context, selected *Account) (*Account, error) {
	if selected == nil {
		return nil, fmt.Errorf("selected account is nil")
	}
	if s == nil || s.accountRepo == nil {
		// 兼容不注入仓库的独立单元测试；生产服务始终注入权威账号仓库。
		if selected.IsShadow() {
			return nil, fmt.Errorf("account repository is required for shadow account %d", selected.ID)
		}
		return selected, nil
	}

	authoritative, err := s.accountRepo.GetByID(ctx, selected.ID)
	if err != nil {
		return nil, fmt.Errorf("load authoritative account %d: %w", selected.ID, err)
	}
	if authoritative == nil {
		return nil, fmt.Errorf("authoritative account %d not found", selected.ID)
	}
	if authoritative.ID != selected.ID {
		return nil, fmt.Errorf("authoritative account id mismatch: got %d, want %d", authoritative.ID, selected.ID)
	}

	credentialAccount, err := resolveCredentialAccount(ctx, s.accountRepo, authoritative)
	if err != nil {
		return nil, err
	}
	if credentialAccount == nil {
		return nil, fmt.Errorf("credential account for selected account %d not found", selected.ID)
	}
	return credentialAccount, nil
}
