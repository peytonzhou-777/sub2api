package repository

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestEnsureOpenAIAccountPersonaArchitectureReady(t *testing.T) {
	t.Run("ready version passes", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		mock.ExpectQuery("SELECT state, architecture_version").
			WillReturnRows(sqlmock.NewRows([]string{"state", "architecture_version"}).AddRow("ready", openAIAccountPersonaArchitectureVersion))
		require.NoError(t, EnsureOpenAIAccountPersonaArchitectureReady(context.Background(), db))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("pending populated database fails closed", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		mock.ExpectQuery("SELECT state, architecture_version").
			WillReturnRows(sqlmock.NewRows([]string{"state", "architecture_version"}).AddRow("pending", openAIAccountPersonaArchitectureVersion))
		mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM accounts").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		err = EnsureOpenAIAccountPersonaArchitectureReady(context.Background(), db)
		require.Error(t, err)
		require.Contains(t, err.Error(), "openai-persona-migrate")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("fresh database activates architecture", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		mock.ExpectQuery("SELECT state, architecture_version").
			WillReturnRows(sqlmock.NewRows([]string{"state", "architecture_version"}).AddRow("pending", openAIAccountPersonaArchitectureVersion))
		mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM accounts").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectExec("UPDATE openai_persona_architecture_state").
			WithArgs(openAIAccountPersonaArchitectureVersion).
			WillReturnResult(sqlmock.NewResult(0, 1))
		require.NoError(t, EnsureOpenAIAccountPersonaArchitectureReady(context.Background(), db))
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
