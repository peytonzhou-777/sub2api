//go:build unit

package service

import (
	"context"
	"strconv"
	"testing"

	dbtrigger "github.com/Wei-Shaw/sub2api/ent/usercreditgranteventtrigger"
	dblimited "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditgrant"
	"github.com/stretchr/testify/require"
)

func TestAssignSignupEntitlementsGrantsLegacyRegistrationEventOnce(t *testing.T) {
	ctx := context.Background()
	adminService, client := newCreditGrantEventTestService(t)
	days := 3600
	event, err := adminService.CreateCreditGrantEvent(ctx, CreditGrantEventInput{
		Name:         "粥站老用户赠额",
		CreditType:   CreditGrantEventTypeLimited,
		Amount:       10,
		ValidityDays: &days,
	})
	require.NoError(t, err)
	userRow := createCreditGrantEventTestUser(t, ctx, client, RoleUser, StatusActive)

	repo := &registrationControlUserRepoStub{
		eligibility: &RegistrationLegacyEligibility{Eligible: true},
	}
	settings := registrationControlSettings("0")
	settings[SettingKeyLegacyRegistrationGrantEventID] = strconv.FormatInt(event.ID, 10)
	authService := newRegistrationControlService(settings, repo)
	authService.entClient = client

	authService.assignSignupEntitlements(ctx, userRow.ID, userRow.Email, signupGrantPlan{})
	authService.assignSignupEntitlements(ctx, userRow.ID, userRow.Email, signupGrantPlan{})

	triggerCount, err := client.UserCreditGrantEventTrigger.Query().
		Where(dbtrigger.EventIDEQ(event.ID), dbtrigger.UserIDEQ(userRow.ID)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, triggerCount)
	grantCount, err := client.UserLimitedCreditGrant.Query().
		Where(dblimited.UserIDEQ(userRow.ID), dblimited.SourceIDEQ(event.ID)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, grantCount)
	normalizedEmail := NormalizeRegistrationEligibilityEmail(userRow.Email)
	require.Equal(t, []string{normalizedEmail, normalizedEmail}, repo.eligibilityLookups)
}

func TestAssignSignupEntitlementsSkipsCyberPolicyWarning(t *testing.T) {
	ctx := context.Background()
	adminService, client := newCreditGrantEventTestService(t)
	days := 3600
	event, err := adminService.CreateCreditGrantEvent(ctx, CreditGrantEventInput{
		Name:         "粥站老用户赠额",
		CreditType:   CreditGrantEventTypeLimited,
		Amount:       10,
		ValidityDays: &days,
	})
	require.NoError(t, err)
	userRow := createCreditGrantEventTestUser(t, ctx, client, RoleUser, StatusActive)

	repo := &registrationControlUserRepoStub{
		eligibility: &RegistrationLegacyEligibility{
			Eligible:       false,
			FailureReasons: []string{"cyber_policy_warning"},
		},
	}
	settings := registrationControlSettings("0")
	settings[SettingKeyLegacyRegistrationGrantEventID] = strconv.FormatInt(event.ID, 10)
	authService := newRegistrationControlService(settings, repo)
	authService.entClient = client

	authService.assignSignupEntitlements(ctx, userRow.ID, userRow.Email, signupGrantPlan{})

	triggerCount, err := client.UserCreditGrantEventTrigger.Query().
		Where(dbtrigger.EventIDEQ(event.ID), dbtrigger.UserIDEQ(userRow.ID)).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, triggerCount)
	grantCount, err := client.UserLimitedCreditGrant.Query().
		Where(dblimited.UserIDEQ(userRow.ID), dblimited.SourceIDEQ(event.ID)).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, grantCount)
}
