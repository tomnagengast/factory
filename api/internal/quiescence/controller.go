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

const (
	ReasonReleased = "released"
	ReasonExpired  = "expired"
	ReasonCanceled = "canceled"
)

type Lease struct {
	Token     string    `json:"lease"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Hooks struct {
	Quiescing func(workflowActive int) error
	Quiesced  func(workflowActive int) error
	Resuming  func(reason string, workflowActive int) error
}

type Controller struct {
	mu       sync.Mutex
	active   int
	lease    *Lease
	quiesced bool
	timer    *time.Timer
	failure  error
	changed  chan struct{}
	now      func() time.Time
	token    func() (string, error)
	hooks    Hooks
}

func New(hooks Hooks) *Controller {
	return &Controller{
		changed: make(chan struct{}),
		now:     time.Now,
		token:   newToken,
		hooks:   hooks,
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
	if c.failure == nil {
		if err := c.quiesceLocked(); err != nil {
			c.failLocked(err)
			return
		}
	}
	c.notifyLocked()
}

func (c *Controller) Acquire(ctx context.Context, ttl time.Duration) (Lease, error) {
	if ttl <= 0 {
		return Lease{}, errors.New("quiescence lease duration must be positive")
	}
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	token, err := c.token()
	if err != nil {
		return Lease{}, err
	}
	c.mu.Lock()
	if c.failure != nil {
		failure := c.failure
		c.mu.Unlock()
		return Lease{}, drainError(failure)
	}
	if c.lease != nil {
		c.mu.Unlock()
		return Lease{}, ErrAlreadyHeld
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return Lease{}, err
	}
	lease := Lease{Token: token, ExpiresAt: c.now().Add(ttl)}
	c.lease = &lease
	c.quiesced = false
	if err := c.callQuiescingLocked(); err != nil {
		c.failLocked(err)
		c.mu.Unlock()
		return Lease{}, drainError(err)
	}
	if err := c.quiesceLocked(); err != nil {
		c.failLocked(err)
		c.mu.Unlock()
		return Lease{}, drainError(err)
	}
	c.timer = time.AfterFunc(time.Until(lease.ExpiresAt), func() {
		_, _ = c.release(token, ReasonExpired)
	})
	c.notifyLocked()
	c.mu.Unlock()

	for {
		c.mu.Lock()
		if c.failure != nil {
			failure := c.failure
			c.mu.Unlock()
			return Lease{}, drainError(failure)
		}
		if c.lease == nil || c.lease.Token != token {
			c.mu.Unlock()
			return Lease{}, ErrExpired
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			_, releaseErr := c.releaseLocked(token, ReasonCanceled)
			c.mu.Unlock()
			if releaseErr != nil {
				return Lease{}, releaseErr
			}
			return Lease{}, ctxErr
		}
		if !c.now().Before(c.lease.ExpiresAt) {
			_, releaseErr := c.releaseLocked(token, ReasonExpired)
			c.mu.Unlock()
			if releaseErr != nil {
				return Lease{}, releaseErr
			}
			return Lease{}, ErrExpired
		}
		if c.active == 0 {
			if err := c.quiesceLocked(); err != nil {
				c.failLocked(err)
				c.mu.Unlock()
				return Lease{}, drainError(err)
			}
			result := *c.lease
			c.mu.Unlock()
			return result, nil
		}
		changed := c.changed
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			_, releaseErr := c.release(token, ReasonCanceled)
			if releaseErr != nil {
				return Lease{}, releaseErr
			}
			return Lease{}, ctx.Err()
		case <-changed:
		}
	}
}

func (c *Controller) Release(token string) (bool, error) {
	return c.release(token, ReasonReleased)
}

func (c *Controller) release(token, reason string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.releaseLocked(token, reason)
}

func (c *Controller) releaseLocked(token, reason string) (bool, error) {
	if c.lease == nil || c.lease.Token != token {
		return false, nil
	}
	if c.failure != nil {
		return true, drainError(c.failure)
	}
	if err := c.quiesceLocked(); err != nil {
		c.failLocked(err)
		return true, drainError(err)
	}
	if c.hooks.Resuming != nil {
		if err := c.hooks.Resuming(reason, c.active); err != nil {
			c.failLocked(err)
			return true, drainError(err)
		}
	}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.lease = nil
	c.quiesced = false
	c.notifyLocked()
	return true, nil
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

func (c *Controller) callQuiescingLocked() error {
	if c.hooks.Quiescing == nil {
		return nil
	}
	return c.hooks.Quiescing(c.active)
}

func (c *Controller) callQuiescedLocked() error {
	if c.hooks.Quiesced == nil {
		return nil
	}
	return c.hooks.Quiesced(c.active)
}

func (c *Controller) quiesceLocked() error {
	if c.lease == nil || c.active != 0 || c.quiesced {
		return nil
	}
	if err := c.callQuiescedLocked(); err != nil {
		return err
	}
	c.quiesced = true
	return nil
}

func (c *Controller) failLocked(err error) {
	if c.failure == nil {
		c.failure = err
	}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.notifyLocked()
}

func (c *Controller) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func drainError(failure error) error {
	return fmt.Errorf("%w: %v", ErrDrainFailed, failure)
}
