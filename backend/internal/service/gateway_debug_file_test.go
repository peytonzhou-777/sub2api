package service

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitDebugGatewayBodyFileNormalizesPath(t *testing.T) {
	tempDir := t.TempDir()
	svc := &GatewayService{}
	svc.initDebugGatewayBodyFile(filepath.Join(tempDir, "nested", "..", "gateway.log"))

	file := svc.debugGatewayBodyFile.Load()
	require.NotNil(t, file)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	require.Equal(t, filepath.Join(tempDir, "gateway.log"), file.Name())
}
