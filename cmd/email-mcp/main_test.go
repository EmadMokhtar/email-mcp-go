package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/version"
)

func TestVersionCommand(t *testing.T) {
	var out, errOut bytes.Buffer

	code := run(t.Context(), []string{"version"}, &out, &errOut)

	require.Equal(t, 0, code)
	require.Contains(t, out.String(), version.Version)
	require.Empty(t, errOut.String())
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer

	code := run(t.Context(), []string{"nope"}, &out, &errOut)

	require.Equal(t, 2, code)
	require.Contains(t, errOut.String(), `unknown command "nope"`)
	require.Empty(t, out.String())
}

func TestNoCommandPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer

	code := run(t.Context(), nil, &out, &errOut)

	require.Equal(t, 2, code)
	require.Contains(t, errOut.String(), "Usage:")
	require.Empty(t, out.String())
}

func TestHelpPrintsUsageToStdout(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			var out, errOut bytes.Buffer

			code := run(t.Context(), []string{arg}, &out, &errOut)

			require.Equal(t, 0, code)
			require.Contains(t, out.String(), "Usage:")
			require.Contains(t, out.String(), "version")
			require.Empty(t, errOut.String())
		})
	}
}

func TestVersionRejectsExtraArguments(t *testing.T) {
	var out, errOut bytes.Buffer

	code := run(t.Context(), []string{"version", "extra"}, &out, &errOut)

	require.Equal(t, 2, code)
	require.Contains(t, errOut.String(), "unexpected argument")
	require.Empty(t, out.String())
}
