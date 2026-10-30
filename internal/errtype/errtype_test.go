package errtype

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChain(t *testing.T) {
	secret := errors.New("secret text from a mail server")

	require.Empty(t, Chain(nil))
	require.Equal(t, "*errors.errorString", Chain(secret))
	require.Equal(t, "*fmt.wrapError>*errors.errorString", Chain(fmt.Errorf("read: %w", secret)))
	require.Equal(t, "*fmt.wrapError>context.deadlineExceededError",
		Chain(fmt.Errorf("fetch: %w", context.DeadlineExceeded)))
	require.NotContains(t, Chain(fmt.Errorf("read: %w", secret)), "secret", "only type names, never the text")
}

func TestChainStopsAtSixTypes(t *testing.T) {
	err := errors.New("base")
	for range 10 {
		err = fmt.Errorf("wrap: %w", err)
	}
	require.Equal(t, "*fmt.wrapError>*fmt.wrapError>*fmt.wrapError>*fmt.wrapError>*fmt.wrapError>*fmt.wrapError", Chain(err))
}
