package imap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClampLimit(t *testing.T) {
	// A negative limit used to reach the slice expression directly, where
	// ids[len(ids)-(-1):] panics with an out-of-range index.
	assert.Equal(t, maxSearchResults, clampLimit(-1))
	assert.Equal(t, maxSearchResults, clampLimit(0))
	assert.Equal(t, maxSearchResults, clampLimit(maxSearchResults+1))
	assert.Equal(t, 1, clampLimit(1))
	assert.Equal(t, 25, clampLimit(25))
	assert.Equal(t, maxSearchResults, clampLimit(maxSearchResults))
}
