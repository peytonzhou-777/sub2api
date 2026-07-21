//go:build unit

package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDefaultLimitedCreditsValueOrDefault_OmittedKeepsExistingValue(t *testing.T) {
	fallback := []service.DefaultLimitedCreditSetting{{Amount: 2.5, ValidityDays: 30}}

	got := defaultLimitedCreditsValueOrDefault(nil, fallback)

	require.Equal(t, fallback, got)
	got[0].Amount = 99
	require.Equal(t, 2.5, fallback[0].Amount)
}

func TestDefaultLimitedCreditsValueOrDefault_EmptyArrayClearsValue(t *testing.T) {
	fallback := []service.DefaultLimitedCreditSetting{{Amount: 2.5, ValidityDays: 30}}
	empty := []dto.DefaultLimitedCreditSetting{}

	got := defaultLimitedCreditsValueOrDefault(&empty, fallback)

	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestDefaultLimitedCreditsValueOrDefault_PreservesDuplicateGrants(t *testing.T) {
	input := []dto.DefaultLimitedCreditSetting{
		{Amount: 1.25, ValidityDays: 30},
		{Amount: 1.25, ValidityDays: 30},
	}

	got := defaultLimitedCreditsValueOrDefault(&input, nil)

	require.Equal(t, []service.DefaultLimitedCreditSetting{
		{Amount: 1.25, ValidityDays: 30},
		{Amount: 1.25, ValidityDays: 30},
	}, got)
}

func TestDiffSettings_ReportsDefaultLimitedCredits(t *testing.T) {
	before := &service.SystemSettings{}
	after := &service.SystemSettings{
		DefaultLimitedCredits: []service.DefaultLimitedCreditSetting{
			{Amount: 5, ValidityDays: 7},
		},
	}

	changed := diffSettings(before, after, nil, nil, UpdateSettingsRequest{})

	require.Contains(t, changed, "default_limited_credits")
}
