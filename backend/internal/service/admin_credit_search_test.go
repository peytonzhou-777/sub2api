//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type creditSearchUserRepoStub struct {
	userRepoStub
	calls   int
	filters UserListFilters
	err     error
}

func (s *creditSearchUserRepoStub) ListWithFilters(_ context.Context, params pagination.PaginationParams, filters UserListFilters) ([]User, *pagination.PaginationResult, error) {
	s.calls++
	s.filters = filters
	if s.err != nil {
		return nil, nil, s.err
	}
	return []User{}, &pagination.PaginationResult{Page: params.Page, PageSize: params.PageSize}, nil
}

func TestAdminService_ListCreditUsers_EmptySearchDoesNotLoadDefaultList(t *testing.T) {
	repo := &creditSearchUserRepoStub{}
	svc := &adminServiceImpl{userRepo: repo}

	users, total, err := svc.ListCreditUsers(context.Background(), 1, 20, "   ")
	require.NoError(t, err)
	require.Empty(t, users)
	require.Zero(t, total)
	require.Zero(t, repo.calls)
}

func TestAdminService_ListCreditUsers_UsesRankedIdentitySearchForNumericKeyword(t *testing.T) {
	repo := &creditSearchUserRepoStub{}
	svc := &adminServiceImpl{userRepo: repo}

	users, total, err := svc.ListCreditUsers(context.Background(), 2, 20, " 12345 ")
	require.NoError(t, err)
	require.Empty(t, users)
	require.Zero(t, total)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, "12345", repo.filters.Search)
	require.Equal(t, UserSearchModeRankedIdentity, repo.filters.SearchMode)
}

func TestAdminService_ListCreditUsers_PropagatesRepositoryError(t *testing.T) {
	wantErr := errors.New("database unavailable")
	repo := &creditSearchUserRepoStub{err: wantErr}
	svc := &adminServiceImpl{userRepo: repo}

	_, _, err := svc.ListCreditUsers(context.Background(), 1, 20, "12345")
	require.ErrorIs(t, err, wantErr)
}
