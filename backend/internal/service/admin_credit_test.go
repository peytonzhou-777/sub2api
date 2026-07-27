package service

import (
	"math"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestValidateCreditAmount(t *testing.T) {
	require.NoError(t, validateCreditAmount(0.00000001))
	for _, value := range []float64{0, -1, math.NaN(), math.Inf(1), 1e12} {
		require.Error(t, validateCreditAmount(value))
	}
}

func TestAdminLimitedCreditConstants(t *testing.T) {
	require.Equal(t, "admin_manual", LimitedCreditSourceAdminManual)
	require.Equal(t, "revoked", LimitedCreditStatusRevoked)
}

func TestAdminLedgerAmountAlwaysSatisfiesNonNegativeConstraint(t *testing.T) {
	require.Equal(t, 5.0, adminLedgerAmount(-5))
	require.Equal(t, 5.0, adminLedgerAmount(5))
	require.Zero(t, adminLedgerAmount(0))
}

func TestApplyUserLimitedCreditSummaries(t *testing.T) {
	users := []User{{ID: 1}, {ID: 2}}
	grants := []*dbent.UserLimitedCreditGrant{
		{UserID: 1, InitialAmount: 12, UsedAmount: 4},
		{UserID: 1, InitialAmount: 5, UsedAmount: 8, FrozenAmount: 1},
		{UserID: 2, InitialAmount: 6, UsedAmount: 1},
		{UserID: 99, InitialAmount: 100},
	}

	applyUserLimitedCreditSummaries(users, grants)

	require.Equal(t, 8.0, users[0].LimitedRemainingAmount)
	require.Equal(t, 2, users[0].LimitedActiveCount)
	require.Equal(t, 5.0, users[1].LimitedRemainingAmount)
	require.Equal(t, 1, users[1].LimitedActiveCount)
}
