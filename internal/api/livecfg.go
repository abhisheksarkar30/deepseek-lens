package api

import "sync"

// LiveConfig holds the settings a running serve can apply without a restart.
type LiveConfig struct {
	mu            sync.Mutex
	retentionDays int
	hotDays       int
}

func NewLiveConfig(retentionDays, hotDays int) *LiveConfig {
	return &LiveConfig{retentionDays: retentionDays, hotDays: hotDays}
}

func (c *LiveConfig) RetentionDays() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retentionDays
}

func (c *LiveConfig) SetRetentionDays(n int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.retentionDays = n
	c.mu.Unlock()
}

func (c *LiveConfig) HotDays() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hotDays
}

// Apply sets retention and hot days under one lock.
func (c *LiveConfig) Apply(retentionDays, hotDays int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.retentionDays = retentionDays
	c.hotDays = hotDays
	c.mu.Unlock()
}
