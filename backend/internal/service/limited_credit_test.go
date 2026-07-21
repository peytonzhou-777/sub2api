//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type limitedCreditRepoStub struct {
	grants          []*LimitedCreditGrant
	err             error
	availableAmount float64
	availableErr    error
}

func (s *limitedCreditRepoStub) CreateGrant(_ context.Context, grant *LimitedCreditGrant) (*LimitedCreditGrant, error) {
	if s.err != nil {
		return nil, s.err
	}
	return grant, nil
}

func (s *limitedCreditRepoStub) CreateGrants(_ context.Context, grants []*LimitedCreditGrant) ([]LimitedCreditGrant, error) {
	s.grants = append([]*LimitedCreditGrant(nil), grants...)
	if s.err != nil {
		return nil, s.err
	}
	result := make([]LimitedCreditGrant, 0, len(grants))
	for index, grant := range grants {
		copy := *grant
		copy.ID = int64(index + 1)
		result = append(result, copy)
	}
	return result, nil
}

func (s *limitedCreditRepoStub) CreateGrantsIndependent(ctx context.Context, grants []*LimitedCreditGrant) ([]LimitedCreditGrant, error) {
	return s.CreateGrants(ctx, grants)
}

func (s *limitedCreditRepoStub) ListActiveByUser(context.Context, int64) ([]LimitedCreditGrant, error) {
	return nil, nil
}

func (s *limitedCreditRepoStub) GetAvailableAmount(context.Context, int64) (float64, error) {
	return s.availableAmount, s.availableErr
}

func TestLimitedCreditService_GrantFromDefaultSettings_CreatesIndependentGrants(t *testing.T) {
	repo := &limitedCreditRepoStub{}
	svc := NewLimitedCreditService(repo)
	before := time.Now().UTC()

	grants, err := svc.GrantFromDefaultSettings(context.Background(), 42, []DefaultLimitedCreditSetting{
		{Amount: 1.25, ValidityDays: 7},
		{Amount: 1.25, ValidityDays: 7},
		{Amount: 8, ValidityDays: 30},
	})

	require.NoError(t, err)
	require.Len(t, grants, 3)
	require.Len(t, repo.grants, 3)
	for index, grant := range repo.grants {
		require.Equal(t, int64(42), grant.UserID)
		require.Equal(t, LimitedCreditSourceDefaultUserSetting, grant.SourceType)
		require.Nil(t, grant.SourceID)
		require.Equal(t, LimitedCreditStatusActive, grant.Status)
		require.Equal(t, "由用户默认设置自动发放", grant.Notes)
		expectedExpiry := before.AddDate(0, 0, []int{7, 7, 30}[index])
		require.WithinDuration(t, expectedExpiry, grant.ExpiresAt, 2*time.Second)
	}
	require.Equal(t, 1.25, repo.grants[0].InitialAmount)
	require.Equal(t, 1.25, repo.grants[1].InitialAmount)
	require.Equal(t, 8.0, repo.grants[2].InitialAmount)
}

func TestLimitedCreditService_GrantFromDefaultSettings_RejectsInvalidConfig(t *testing.T) {
	repo := &limitedCreditRepoStub{}
	svc := NewLimitedCreditService(repo)

	_, err := svc.GrantFromDefaultSettings(context.Background(), 42, []DefaultLimitedCreditSetting{
		{Amount: 0, ValidityDays: 30},
	})

	require.Error(t, err)
	require.Empty(t, repo.grants)
}

func TestLimitedCreditService_GrantFromDefaultSettings_PropagatesBatchFailure(t *testing.T) {
	expected := errors.New("write failed")
	repo := &limitedCreditRepoStub{err: expected}
	svc := NewLimitedCreditService(repo)

	_, err := svc.GrantFromDefaultSettings(context.Background(), 42, []DefaultLimitedCreditSetting{
		{Amount: 5, ValidityDays: 30},
	})

	require.ErrorIs(t, err, expected)
}
