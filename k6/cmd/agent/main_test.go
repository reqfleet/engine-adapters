package main

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLabelLimit(t *testing.T) {
	value, err := labelLimit("", false)
	require.NoError(t, err)
	require.Equal(t, 10, value)
	for _, text := range []string{"0", "1", "10"} {
		value, err = labelLimit(text, true)
		require.NoError(t, err)
		require.GreaterOrEqual(t, value, 0)
	}
	for _, text := range []string{"", "-1", "+1", " 1", "1.0", "null", "9223372036854775808"} {
		_, err = labelLimit(text, true)
		require.Error(t, err, text)
	}
}
