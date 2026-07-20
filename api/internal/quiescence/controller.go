package quiescence

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrAlreadyHeld = errors.New("workflow coordinator is already quiescing")
	ErrExpired     = errors.New("quiescence lease expired before workflows drained")
	ErrDrainFailed = errors.New("workflow coordinator failed while draining")
)

type Lease struct {
	Token     string    `json:"lease"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Controller struct {
	mu      sync.Mutex
	active  int
	lease   *Lease
	timer   *time.Timer
	failure error
	changed chan struct{}
	now     func() time.Time
	token   func() (string, error)
}

func New() *Controller {
	return &Controller{
		changed: make(chan struct{}),
		now:     time.Now,
		token:   newToken,
	}
}

func newToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (c *Controller) TryStart() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lease != nil || c.failure != nil {
		return false
	}
	c.active++
	return true
}

func (c *Controller) Done(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == 0 {
		panic("quiescence admission completed without an active operation")
	}
	c.active--
	if err != nil && c.failure == nil {
		c.failure = err
	}
	c.notifyLocked()
}

func (c *Controller) Acquire(ctx context.Context, ttl time.Duration) (Lease, error) {
	if ttl <= 0 {
		return Lease{}, errors.New("quiescence lease duration must be positive")
	}
	token, err := c.token()
	if err != nil {
		return Lease{}, err
	}
	c.mu.Lock()
	if c.failure != nil {
		failure := c.failure
		c.mu.Unlock()
		return Lease{}, fmt.Errorf("%w: %v", ErrDrainFailed, failure)
	}
	if c.lease != nil {
		c.mu.Unlock()
		return Lease{}, ErrAlreadyHeld
	}
	lease := Lease{Token: token, ExpiresAt: c.now().Add(ttl)}
	c.lease = &lease
	c.timer = time.AfterFunc(ttl, func() {
		c.release(token)
	})
	c.notifyLocked()
	c.mu.Unlock()

	for {
		c.mu.Lock()
		if c.failure != nil {
			failure := c.failure
			c.releaseLocked(token)
			c.mu.Unlock()
			return Lease{}, fmt.Errorf("%w: %v", ErrDrainFailed, failure)
		}
		if c.lease == nil || c.lease.Token != token {
			c.mu.Unlock()
			return Lease{}, ErrExpired
		}
		if c.active == 0 {
			result := *c.lease
			c.mu.Unlock()
			return result, nil
		}
		changed := c.changed
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			c.release(token)
			return Lease{}, ctx.Err()
		case <-changed:
		}
	}
}

func (c *Controller) Release(token string) bool {
	return c.release(token)
}

func (c *Controller) release(token string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lease == nil || c.lease.Token != token {
		return false
	}
	c.releaseLocked(token)
	return true
}

func (c *Controller) releaseLocked(token string) {
	if c.lease == nil || c.lease.Token != token {
		return
	}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.lease = nil
	c.notifyLocked()
}

func (c *Controller) Changes() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.changed
}

func (c *Controller) Active() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

func (c *Controller) Accepting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lease == nil && c.failure == nil
}

func (c *Controller) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}
