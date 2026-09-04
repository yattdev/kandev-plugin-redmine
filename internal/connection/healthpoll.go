package connection

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	"kandev-plugin-redmine/internal/redmineclient"
)

// DefaultHealthInterval/DefaultHealthJitter match the spec's "~90s jittered
// health probe" convention (mirroring kandev-plugin-bitbucket).
const (
	DefaultHealthInterval = 90 * time.Second
	DefaultHealthJitter   = 15 * time.Second
)

// HealthPoller runs its own Start(ctx)/Stop() lifecycle probing every
// connected workspace's stored credentials against Redmine on an interval,
// flipping plugin_state health without ever deleting the stored key on
// failure. There is no host healthpoll equivalent for plugins (plan Risks);
// this is the plugin's own.
type HealthPoller struct {
	svc      *Service
	interval time.Duration
	jitter   time.Duration
	rng      *rand.Rand

	mu       sync.Mutex
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	running  bool
	stopping bool
}

type HealthPollerOption func(*HealthPoller)

func WithInterval(d time.Duration) HealthPollerOption {
	return func(p *HealthPoller) { p.interval = d }
}

func WithJitter(d time.Duration) HealthPollerOption {
	return func(p *HealthPoller) { p.jitter = d }
}

func NewHealthPoller(svc *Service, opts ...HealthPollerOption) *HealthPoller {
	p := &HealthPoller{
		svc:      svc,
		interval: DefaultHealthInterval,
		jitter:   DefaultHealthJitter,
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec // jitter timing, not security-sensitive
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Start launches the poll loop if it is not already running. Idempotent.
func (p *HealthPoller) Start(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running || p.stopping {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.running = true
	p.wg.Add(1)
	go p.run(runCtx)
}

// Stop cancels the poll loop and waits for it to exit. Idempotent; safe to
// call without a prior Start.
func (p *HealthPoller) Stop() {
	p.mu.Lock()
	if !p.running || p.stopping {
		p.mu.Unlock()
		return
	}
	cancel := p.cancel
	p.stopping = true
	p.mu.Unlock()

	cancel()
	p.wg.Wait()
	p.mu.Lock()
	p.running = false
	p.stopping = false
	p.cancel = nil
	p.mu.Unlock()
}

func (p *HealthPoller) run(ctx context.Context) {
	defer p.wg.Done()
	for {
		timer := time.NewTimer(p.nextDelay())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		p.probeAll(ctx)
	}
}

func (p *HealthPoller) nextDelay() time.Duration {
	if p.jitter <= 0 {
		return p.interval
	}
	return p.interval + time.Duration(p.rng.Int63n(int64(p.jitter)))
}

func (p *HealthPoller) probeAll(ctx context.Context) {
	ids, err := p.svc.ListWorkspaceIDs(ctx)
	if err != nil {
		return
	}
	for _, workspaceID := range ids {
		p.probeOne(ctx, workspaceID)
	}
}

func (p *HealthPoller) probeOne(ctx context.Context, workspaceID string) {
	enabled, err := p.svc.GetEnabled(ctx, workspaceID)
	if err != nil || !enabled {
		return
	}
	record, client, found, err := p.svc.clientSnapshot(ctx, workspaceID)
	if err != nil || !found {
		return
	}
	if _, err := client.ValidateCredentials(ctx); err != nil {
		var apiErr *redmineclient.APIError
		if errors.As(err, &apiErr) && (apiErr.Kind == redmineclient.ErrKindInvalidCredentials || apiErr.Kind == redmineclient.ErrKindAPIDisabled) {
			_ = p.svc.markDegraded(ctx, workspaceID, record, err.Error())
		}
		return
	}
	_ = p.svc.markHealthy(ctx, workspaceID, record)
}
