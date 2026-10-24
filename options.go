package metrics

import (
	"errors"
	"fmt"
	"time"
)

// Option configures a DB when it is opened.
type Option func(*config) error

type config struct {
	retentionDays    int
	flushInterval    time.Duration
	minFreeDiskBytes int64
	encryptionKey    []byte
}

// WithRetentionDays configures time-based retention using whole 24-hour days.
// The storage engine removes expired data in the background.
// The default is seven days.
func WithRetentionDays(days int) Option {
	return func(c *config) error {
		if days < 1 || days > 36500 {
			return fmt.Errorf("metrics: retention days must be within 1..36500, got %d", days)
		}
		c.retentionDays = days
		return nil
	}
}

// WithFlushInterval sets how often ingested data is flushed from memory to
// disk. The engine enforces a minimum interval of one second. This setting is
// process-wide: the last DB opened in the process wins. The default interval
// is two seconds.
//
// Newly written samples become searchable within a couple of seconds
// regardless of this setting; Flush forces search visibility immediately.
func WithFlushInterval(d time.Duration) Option {
	return func(c *config) error {
		if d < time.Second {
			return errors.New("metrics: flush interval must be at least one second")
		}
		c.flushInterval = d
		return nil
	}
}

// WithMinFreeDiskBytes enables automatic read-only mode when there is
// insufficient available disk space. This setting is process-wide: the last
// DB opened in the process wins. Use zero to disable (the default).
func WithMinFreeDiskBytes(n int64) Option {
	return func(c *config) error {
		if n < 0 {
			return errors.New("metrics: min free disk bytes cannot be negative")
		}
		c.minFreeDiskBytes = n
		return nil
	}
}

// WithEncryptionKey selects authenticated AES-256-GCM encryption for
// compressed data blocks, indexes and metadata. The 32-byte raw key is never
// persisted. Enabling encryption on an existing plaintext database is rejected.
func WithEncryptionKey(key []byte) Option {
	copied := append([]byte(nil), key...)
	return func(c *config) error {
		if len(copied) != 32 {
			return fmt.Errorf("metrics: AES-256-GCM requires a 32-byte encryption key")
		}
		c.encryptionKey = append([]byte(nil), copied...)
		return nil
	}
}
