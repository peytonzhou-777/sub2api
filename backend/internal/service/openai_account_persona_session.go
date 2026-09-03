package service

import (
	"context"
	"errors"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// PrepareAccountPersonaExecutionTarget 为新根请求惰性轮转并返回完整不可变目标。
// continuationEpoch 非零时只恢复历史 epoch，不允许重映射或创建新 Session。
func (s *OpenAIOAuthService) PrepareAccountPersonaExecutionTarget(
	ctx context.Context,
	accountID, accountPersonaID, continuationEpoch int64,
	now time.Time,
) (OpenAIExecutionTarget, error) {
	if s == nil || s.accountPersonaRepo == nil || accountID <= 0 || accountPersonaID <= 0 {
		return OpenAIExecutionTarget{}, ErrOpenAIPersonaCredentialStoreUnavailable
	}
	if now.IsZero() {
		now = time.Now()
	}
	persona, err := s.accountPersonaRepo.GetAccountPersona(ctx, accountID, accountPersonaID)
	if err != nil {
		return OpenAIExecutionTarget{}, openAIAccountPersonaAPIError(err)
	}
	if continuationEpoch > 0 {
		session, loadErr := s.accountPersonaRepo.GetAccountPersonaSession(ctx, accountID, accountPersonaID, continuationEpoch, now)
		if loadErr != nil {
			return OpenAIExecutionTarget{}, openAIAccountPersonaSessionAPIError(loadErr)
		}
		return OpenAIExecutionTargetFromPersonaSession(*persona, *session)
	}
	policy := defaultCodexFingerprintEpochPolicy()
	if s.settingService != nil {
		policy = s.settingService.GetCodexFingerprintEpochPolicy(ctx)
	}
	sessionID, err := openai.GenerateSessionID()
	if err != nil {
		return OpenAIExecutionTarget{}, err
	}
	prepared, err := s.accountPersonaRepo.PrepareAccountPersonaSession(ctx, OpenAIAccountPersonaSessionPrepareInput{
		AccountID: accountID, AccountPersonaID: accountPersonaID, Now: now,
		Policy: policy, NewUpstreamSession: sessionID,
	})
	if err != nil {
		return OpenAIExecutionTarget{}, openAIAccountPersonaSessionAPIError(err)
	}
	if prepared.Rotated && s.personaTransportInvalidator != nil {
		s.personaTransportInvalidator.InvalidateOpenAIAccountPersonaSessionTransport(
			accountID, accountPersonaID, prepared.Session.SessionEpoch-1,
		)
	}
	return OpenAIExecutionTargetFromPersonaSession(prepared.Persona, prepared.Session)
}

// RotateAccountPersonaSession 执行管理员普通或安全强制轮转。
func (s *OpenAIOAuthService) RotateAccountPersonaSession(
	ctx context.Context,
	accountID, accountPersonaID, expectedRowVersion int64,
	force bool,
) (*OpenAIAccountPersonaSessionPrepareResult, error) {
	if s == nil || s.accountPersonaRepo == nil {
		return nil, ErrOpenAIPersonaCredentialStoreUnavailable
	}
	policy := defaultCodexFingerprintEpochPolicy()
	if s.settingService != nil {
		policy = s.settingService.GetCodexFingerprintEpochPolicy(ctx)
	}
	sessionID, err := openai.GenerateSessionID()
	if err != nil {
		return nil, err
	}
	prepared, err := s.accountPersonaRepo.PrepareAccountPersonaSession(ctx, OpenAIAccountPersonaSessionPrepareInput{
		AccountID: accountID, AccountPersonaID: accountPersonaID,
		ExpectedRowVersion: expectedRowVersion, Now: time.Now(), Policy: policy,
		NewUpstreamSession: sessionID, Manual: true, Force: force,
	})
	if err != nil {
		return nil, openAIAccountPersonaSessionAPIError(err)
	}
	if prepared.Rotated && s.personaTransportInvalidator != nil {
		s.personaTransportInvalidator.InvalidateOpenAIAccountPersonaSessionTransport(
			accountID, accountPersonaID, prepared.Session.SessionEpoch-1,
		)
	}
	return prepared, nil
}

// TouchAccountPersonaSession 只在有效上游输出后推进 epoch 活跃时间。
func (s *OpenAIOAuthService) TouchAccountPersonaSession(ctx context.Context, target OpenAIExecutionTarget, now time.Time) error {
	if s == nil || s.accountPersonaRepo == nil || !target.Valid() {
		return ErrOpenAIAccountPersonaIdentityMismatch
	}
	if now.IsZero() {
		now = time.Now()
	}
	return s.accountPersonaRepo.TouchAccountPersonaSession(ctx, target.AccountPersonaID, target.SessionEpoch, now)
}

func openAIAccountPersonaSessionAPIError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrOpenAIAccountPersonaSessionOccupied):
		return infraerrors.Conflict("OPENAI_PERSONA_SESSION_OCCUPIED", "AccountPersona Session is occupied")
	case errors.Is(err, ErrOpenAIAccountPersonaSessionNotFound), errors.Is(err, ErrOpenAIAccountPersonaSessionExpired):
		return infraerrors.Conflict("OPENAI_PERSONA_CONTINUATION_EXPIRED", "AccountPersona Session continuation has expired")
	case errors.Is(err, ErrOpenAIAccountPersonaCASConflict):
		return infraerrors.Conflict("OPENAI_PERSONA_VERSION_CONFLICT", "AccountPersona has changed")
	case errors.Is(err, ErrOpenAIAccountPersonaNotFound):
		return infraerrors.NotFound("OPENAI_PERSONA_NOT_FOUND", "AccountPersona not found")
	default:
		return err
	}
}
