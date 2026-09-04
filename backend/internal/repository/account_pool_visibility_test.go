package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetAccountPoolGroupAccountIDs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("创建 sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	repo := newGroupRepositoryWithSQL(nil, db)
	mock.ExpectQuery(`SELECT group_id, account_id`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "account_id"}).
			AddRow(int64(1), int64(10)).
			AddRow(int64(1), int64(11)).
			AddRow(int64(2), int64(11)))

	result, err := repo.GetAccountPoolGroupAccountIDs(context.Background(), []int64{1, 2})
	if err != nil {
		t.Fatalf("读取号池分组账号映射: %v", err)
	}
	if len(result[1]) != 2 || result[1][0] != 10 || result[1][1] != 11 || len(result[2]) != 1 || result[2][0] != 11 {
		t.Fatalf("分组账号映射不完整: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL 预期未满足: %v", err)
	}
}

func TestGetAccountPoolDefaultGroupID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("创建 sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	repo := newAPIKeyRepositoryWithSQL(nil, db)
	mock.ExpectQuery(`SELECT MIN\(group_id\)`).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(int64(7)))

	groupID, err := repo.GetAccountPoolDefaultGroupID(context.Background(), 42)
	if err != nil {
		t.Fatalf("读取号池默认分组: %v", err)
	}
	if groupID == nil || *groupID != 7 {
		t.Fatalf("默认分组错误: %v", groupID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL 预期未满足: %v", err)
	}
}

func TestGetAccountPoolDefaultGroupIDReturnsNilWithoutUniformGroup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("创建 sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	repo := newAPIKeyRepositoryWithSQL(nil, db)
	mock.ExpectQuery(`SELECT MIN\(group_id\)`).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"min"}))

	groupID, err := repo.GetAccountPoolDefaultGroupID(context.Background(), 42)
	if err != nil {
		t.Fatalf("读取号池默认分组: %v", err)
	}
	if groupID != nil {
		t.Fatalf("混合、未分组或无密钥时不应返回默认分组: %v", *groupID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL 预期未满足: %v", err)
	}
}
