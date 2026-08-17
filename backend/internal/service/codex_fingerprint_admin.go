package service

import (
	"context"
	"net/http"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var ErrCodexFingerprintAccountUnsupported = infraerrors.BadRequest(
	"CODEX_FINGERPRINT_ACCOUNT_UNSUPPORTED",
	"codex fingerprint management requires an OpenAI OAuth account",
)

var ErrCodexFingerprintDisabled = infraerrors.BadRequest(
	"CODEX_FINGERPRINT_DISABLED",
	"codex fingerprint convergence is disabled for this account",
)

// CodexFingerprintAdminStatus 仅暴露安全的运行统计，不返回 seed 或原始标识。
type CodexFingerprintAdminStatus struct {
	AccountID         int64      `json:"account_id"`
	Mode              string     `json:"mode"`
	AlgorithmVersion  string     `json:"algorithm_version"`
	AccountEpoch      int64      `json:"account_epoch"`
	EpochStartedAt    *time.Time `json:"epoch_started_at,omitempty"`
	SessionScopeCount int64      `json:"session_scope_count"`
	ThreadCount       int64      `json:"thread_count"`
	LegacyThreadCount int64      `json:"legacy_thread_count"`
	RotationCount     int64      `json:"rotation_count"`
	SecretID          string     `json:"secret_id,omitempty"`
}

// CodexFingerprintAdminRepository 提供与调度状态隔离的指纹管理能力。
type CodexFingerprintAdminRepository interface {
	GetCodexFingerprintAdminStatus(ctx context.Context, accountID int64) (*CodexFingerprintAdminStatus, error)
	RotateCodexFingerprintSessions(ctx context.Context, accountID int64, now time.Time) error
}

// CodexFingerprintAdminService 是管理员 handler 使用的窄接口。
type CodexFingerprintAdminService interface {
	GetCodexFingerprintStatus(ctx context.Context, accountID int64) (*CodexFingerprintAdminStatus, error)
	RotateCodexFingerprint(ctx context.Context, accountID int64) (*CodexFingerprintAdminStatus, error)
	DisableCodexFingerprint(ctx context.Context, accountID int64) (*CodexFingerprintAdminStatus, error)
}

func (s *adminServiceImpl) codexFingerprintAdminRepository() (CodexFingerprintAdminRepository, error) {
	repo, ok := s.accountRepo.(CodexFingerprintAdminRepository)
	if !ok {
		return nil, infraerrors.New(http.StatusInternalServerError, "CODEX_FINGERPRINT_ADMIN_UNAVAILABLE", "codex fingerprint admin repository unavailable")
	}
	return repo, nil
}

func (s *adminServiceImpl) GetCodexFingerprintStatus(ctx context.Context, accountID int64) (*CodexFingerprintAdminStatus, error) {
	repo, err := s.codexFingerprintAdminRepository()
	if err != nil {
		return nil, err
	}
	return repo.GetCodexFingerprintAdminStatus(ctx, accountID)
}

// RotateCodexFingerprint 只推进指纹 epoch，不接触账号调度或用户粘性 placement。
func (s *adminServiceImpl) RotateCodexFingerprint(ctx context.Context, accountID int64) (*CodexFingerprintAdminStatus, error) {
	repo, err := s.codexFingerprintAdminRepository()
	if err != nil {
		return nil, err
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil || !account.IsOpenAIOAuth() {
		return nil, ErrCodexFingerprintAccountUnsupported
	}
	if account.GetCodexFingerprintMode() == codexFingerprintOff {
		return nil, ErrCodexFingerprintDisabled
	}
	now := time.Now()
	state := CodexFingerprintState{
		Seed: account.CodexFingerprintSeed, Version: account.CodexFingerprintVersion,
		Epoch: account.CodexFingerprintEpoch, EpochStartedAt: derefTime(account.CodexFingerprintEpochStartedAt),
	}
	if !state.valid() {
		stateRepo, ok := s.accountRepo.(CodexFingerprintStateRepository)
		if !ok {
			return nil, infraerrors.New(http.StatusInternalServerError, "CODEX_FINGERPRINT_ADMIN_UNAVAILABLE", "codex fingerprint state repository unavailable")
		}
		if _, err := stateRepo.GetOrInitializeCodexFingerprintState(ctx, accountID, now); err != nil {
			return nil, err
		}
	}
	if err := repo.RotateCodexFingerprintSessions(ctx, accountID, now); err != nil {
		return nil, err
	}
	return repo.GetCodexFingerprintAdminStatus(ctx, accountID)
}

// DisableCodexFingerprint 提供明确回滚入口，仅把 mode 改为 off。
func (s *adminServiceImpl) DisableCodexFingerprint(ctx context.Context, accountID int64) (*CodexFingerprintAdminStatus, error) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil || !account.IsOpenAIOAuth() {
		return nil, ErrCodexFingerprintAccountUnsupported
	}
	if err := s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintOff)}); err != nil {
		return nil, err
	}
	repo, err := s.codexFingerprintAdminRepository()
	if err != nil {
		return nil, err
	}
	return repo.GetCodexFingerprintAdminStatus(ctx, accountID)
}
