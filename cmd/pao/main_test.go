package main

import (
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVersion(t *testing.T) {
	parser, err := kong.New(&CLI{}, kong.Name("pao"))
	require.NoError(t, err)

	ctx, err := parser.Parse([]string{"version"})
	require.NoError(t, err)
	assert.Equal(t, "version", ctx.Command())
}

func TestBuildVersionNotEmpty(t *testing.T) {
	assert.NotEmpty(t, buildVersion())
}
