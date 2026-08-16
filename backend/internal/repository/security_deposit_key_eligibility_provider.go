package repository

import (
	"database/sql"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.SecurityDepositKeyEligibilityRepository = (*apiKeyRepository)(nil)

// NewSecurityDepositKeyEligibilityRepository 提供保证金资格重算所需的窄仓储视图。
func NewSecurityDepositKeyEligibilityRepository(client *dbent.Client, sqlDB *sql.DB) service.SecurityDepositKeyEligibilityRepository {
	return newAPIKeyRepositoryWithSQL(client, sqlDB)
}
