//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type registrationControlUserRepoStub struct {
	UserRepository
	count                int64
	eligibility          *RegistrationLegacyEligibility
	eligibilityLookups   []string
	createdUserLimit     int64
	createdDomain        string
	createdWithGuardCall int
}

func (s *registrationControlUserRepoStub) CountRegistrationUsers(context.Context) (int64, error) {
	return s.count, nil
}

func (s *registrationControlUserRepoStub) GetRegistrationLegacyEligibility(_ context.Context, email string) (*RegistrationLegacyEligibility, error) {
	s.eligibilityLookups = append(s.eligibilityLookups, email)
	return s.eligibility, nil
}

func (s *registrationControlUserRepoStub) GetRegistrationEligibilityStats(context.Context) (*RegistrationEligibilityStats, error) {
	return &RegistrationEligibilityStats{}, nil
}

func (s *registrationControlUserRepoStub) CreateWithRegistrationGuards(_ context.Context, _ *User, domain string, userLimit int64) error {
	s.createdWithGuardCall++
	s.createdDomain = domain
	s.createdUserLimit = userLimit
	return nil
}

func newRegistrationControlService(values map[string]string, repo *registrationControlUserRepoStub) *AuthService {
	return &AuthService{
		settingService: NewSettingService(&settingRepoStub{values: values}, nil),
		userRepo:       repo,
	}
}

func registrationControlSettings(limit string) map[string]string {
	return map[string]string{
		SettingKeyRegistrationEnabled:                 "true",
		SettingKeyRegistrationUserLimit:               limit,
		SettingKeyInvitationCodeEnabled:               "true",
		SettingKeyLegacyInvitationExemptionEnabled:    "true",
		SettingKeyRegistrationEmailSuffixWhitelist:    "[]",
		SettingKeyRegistrationEmailDomainQuotaEnabled: "false",
	}
}

func TestCheckRegistrationLegacyEligibilityChecksCapacityBeforeEmail(t *testing.T) {
	repo := &registrationControlUserRepoStub{
		count:       10,
		eligibility: &RegistrationLegacyEligibility{Eligible: true},
	}
	svc := newRegistrationControlService(registrationControlSettings("10"), repo)

	result, err := svc.CheckRegistrationLegacyEligibility(context.Background(), "eligible@example.com")

	require.NoError(t, err)
	require.False(t, result.Eligible)
	require.Equal(t, []string{"REGISTRATION_CAPACITY_REACHED"}, result.ReasonCodes)
	require.Empty(t, repo.eligibilityLookups, "容量已满时不得查询或泄露邮箱资格")
}

func TestCheckRegistrationLegacyEligibilityMapsImportedReasons(t *testing.T) {
	repo := &registrationControlUserRepoStub{
		count: 9,
		eligibility: &RegistrationLegacyEligibility{
			FailureReasons: []string{"insufficient_success_calls", "cyber_policy_warning"},
		},
	}
	svc := newRegistrationControlService(registrationControlSettings("10"), repo)

	result, err := svc.CheckRegistrationLegacyEligibility(context.Background(), "  Legacy.User@Example.COM ")

	require.NoError(t, err)
	require.False(t, result.Eligible)
	require.ElementsMatch(t, []string{
		RegistrationEligibilityReasonInsufficientSuccessCalls,
		RegistrationEligibilityReasonCyberPolicyWarning,
	}, result.ReasonCodes)
	require.Equal(t, []string{"legacy.user@example.com"}, repo.eligibilityLookups)
}

func TestCreateUserWithRegistrationGuardInvitationBypassesCapacity(t *testing.T) {
	repo := &registrationControlUserRepoStub{count: 10}
	svc := newRegistrationControlService(registrationControlSettings("10"), repo)
	user := &User{Email: "invited@example.com"}

	require.NoError(t, svc.createUserWithRegistrationEmailGuard(context.Background(), user, true))
	require.Equal(t, 1, repo.createdWithGuardCall)
	require.Zero(t, repo.createdUserLimit, "真实邀请码路径必须把容量限制关闭")
}

func TestCreateUserWithRegistrationGuardAppliesCapacityToExemptEmail(t *testing.T) {
	repo := &registrationControlUserRepoStub{count: 9}
	svc := newRegistrationControlService(registrationControlSettings("10"), repo)
	user := &User{Email: "legacy@example.com"}

	require.NoError(t, svc.createUserWithRegistrationEmailGuard(context.Background(), user, false))
	require.Equal(t, int64(10), repo.createdUserLimit)
}

func TestNormalizeRegistrationEligibilityEmailMatchesOperationsCSVContract(t *testing.T) {
	require.Equal(t, "yuqi.li.91@gmail.com", NormalizeRegistrationEligibilityEmail("  YuQi.Li.91@GMAIL.COM "))
	require.Equal(t, "siumabon123tw+congee@gmail.com", NormalizeRegistrationEligibilityEmail("Siumabon123tw+congee@gmail.com"))
}
