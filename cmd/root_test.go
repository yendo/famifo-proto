package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCmd(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	cmd := NewCmdRoot(&buf)
	err := cmd.Execute()

	require.NoError(t, err)
	assert.NotEqual(t, "", buf.String())
}
