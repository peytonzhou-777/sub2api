package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAITurnStateCipherScopesAndOpaqueFormat(t *testing.T) {
	c := newOpenAITurnStateCipher(&config.Config{JWT: config.JWTConfig{Secret: "test-secret"}})
	token, err := c.wrap("upstream-state", turnStateAAD(7, "session-a"))
	require.NoError(t, err)
	require.Regexp(t, `^ts1\.[A-Za-z0-9_-]+$`, token)
	got, err := c.unwrap(token, turnStateAAD(7, "session-a"))
	require.NoError(t, err)
	require.Equal(t, "upstream-state", got)
	_, err = c.unwrap(token, turnStateAAD(8, "session-a"))
	require.Error(t, err)
}
