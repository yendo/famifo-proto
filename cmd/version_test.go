package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionCmd(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	//buf := bytes.NewBuffer([]byte{})

	cmd := NewCmdVersion(&buf)
	err := cmd.Execute()

	require.NoError(t, err)
	assert.Equal(t, "version called!", buf.String())
}
