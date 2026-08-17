package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanCmd(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	cmd := NewCmdScan(&buf)
	err := cmd.Execute()

	require.NoError(t, err)
	assert.Equal(t, "scan!", buf.String())
}
