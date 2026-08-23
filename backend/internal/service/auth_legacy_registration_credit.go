package service

import (
	"context"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// grantLegacyRegistrationCredit 为符合导入资格的新注册老用户幂等触发配置的赠额事件。
func (s *AuthService) grantLegacyRegistrationCredit(ctx context.Context, userID int64, email string) {
	if s == nil || s.entClient == nil || s.settingService == nil || userID <= 0 {
		return
	}
	settings, err := s.settingService.GetRegistrationControlSettings(ctx)
	if err != nil {
		logger.LegacyPrintf("service.auth", "[Auth] Failed to load legacy registration credit settings: user_id=%d err=%v", userID, err)
		return
	}
	if !settings.LegacyInvitationExemptionEnabled || settings.LegacyRegistrationGrantEventID <= 0 {
		return
	}

	result, err := s.checkRegistrationLegacyEligibilityRecord(ctx, email, true)
	if err != nil {
		logger.LegacyPrintf("service.auth", "[Auth] Failed to verify legacy registration credit eligibility: user_id=%d err=%v", userID, err)
		return
	}
	if !result.Eligible {
		return
	}

	if _, err = triggerCreditGrantEvent(ctx, s.entClient, userID, settings.LegacyRegistrationGrantEventID); err != nil {
		if infraerrors.Reason(err) == "CREDIT_GRANT_EVENT_ALREADY_TRIGGERED" {
			return
		}
		logger.LegacyPrintf("service.auth", "[Auth] Failed to grant legacy registration credit: user_id=%d event_id=%d err=%v", userID, settings.LegacyRegistrationGrantEventID, err)
	}
}
