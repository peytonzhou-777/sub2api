package service

import (
	"math"
	"testing"

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
