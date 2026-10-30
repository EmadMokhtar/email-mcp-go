package imapsmtp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigWithDefaults(t *testing.T) {
	t.Run("zero values get the defaults", func(t *testing.T) {
		var cfg Config
		cfg.withDefaults()

		require.Equal(t, 15*time.Second, cfg.DialTimeout)
		require.Equal(t, 60*time.Second, cfg.CommandTimeout)
		require.Equal(t, 5*time.Minute, cfg.IdleTimeout)
		require.Equal(t, 2, cfg.PoolSize)
	})

	t.Run("negative values get the defaults", func(t *testing.T) {
		cfg := Config{DialTimeout: -1, CommandTimeout: -1, IdleTimeout: -1, PoolSize: -1}
		cfg.withDefaults()

		require.Equal(t, 15*time.Second, cfg.DialTimeout)
		require.Equal(t, 2, cfg.PoolSize)
	})

	t.Run("set values stay", func(t *testing.T) {
		cfg := Config{DialTimeout: time.Second, CommandTimeout: 2 * time.Second, IdleTimeout: 3 * time.Second, PoolSize: 4}
		cfg.withDefaults()

		require.Equal(t, time.Second, cfg.DialTimeout)
		require.Equal(t, 2*time.Second, cfg.CommandTimeout)
		require.Equal(t, 3*time.Second, cfg.IdleTimeout)
		require.Equal(t, 4, cfg.PoolSize)
	})
}
