package judgement_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sky-valley/gitrdone/internal/judgement"
)

func TestMemoryLeasesExpireAndFenceStaleOwners(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	leases := judgement.NewMemoryLeases(func() time.Time { return now })
	key := judgement.WorkKey{RepoID: "repo_a", VersionID: "version_a"}

	first, acquired, err := leases.Acquire(context.Background(), key, "worker-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first acquire = %#v, %t, %v; want acquired", first, acquired, err)
	}
	if _, acquired, err := leases.Acquire(context.Background(), key, "worker-b", time.Minute); err != nil || acquired {
		t.Fatalf("contending acquire = %t, %v; want unavailable", acquired, err)
	}

	now = now.Add(time.Minute + time.Second)
	second, acquired, err := leases.Acquire(context.Background(), key, "worker-b", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire after expiry = %#v, %t, %v; want acquired", second, acquired, err)
	}
	if second.Generation <= first.Generation {
		t.Fatalf("replacement generation = %d, want greater than %d", second.Generation, first.Generation)
	}
	if err := leases.Release(context.Background(), first); !errors.Is(err, judgement.ErrLeaseLost) {
		t.Fatalf("stale release error = %v, want ErrLeaseLost", err)
	}
	if err := leases.Renew(context.Background(), first, time.Minute); !errors.Is(err, judgement.ErrLeaseLost) {
		t.Fatalf("stale renewal error = %v, want ErrLeaseLost", err)
	}
	if err := leases.Release(context.Background(), second); err != nil {
		t.Fatalf("release current lease: %v", err)
	}
}

func TestPendingRunnerProcessesGlobalPendingWorkWithBoundedWorkers(t *testing.T) {
	source := newPendingSource(
		judgement.WorkItem{RepoID: "repo_a", VersionID: "version_a"},
		judgement.WorkItem{RepoID: "repo_b", VersionID: "version_b"},
		judgement.WorkItem{RepoID: "repo_c", VersionID: "version_c"},
	)
	processor := &recordingProcessor{source: source, completed: make(chan struct{}, 3)}
	runner := judgement.NewPendingRunner(
		source,
		judgement.NewMemoryLeases(nil),
		processor,
		judgement.RunnerOptions{Workers: 2, BatchSize: 2, PollInterval: time.Millisecond, LeaseTTL: 100 * time.Millisecond},
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(ctx)
	}()
	for range 3 {
		select {
		case <-processor.completed:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for global pending work")
		}
	}
	cancel()
	<-done

	if got := processor.callCount(); got != 3 {
		t.Fatalf("processor calls = %d, want each pending Version once", got)
	}
	if got := processor.maxConcurrency(); got > 2 {
		t.Fatalf("maximum processor concurrency = %d, want at most 2", got)
	}
	if remaining := source.remaining(); remaining != 0 {
		t.Fatalf("remaining pending work = %d, want none", remaining)
	}
}

func TestPendingRunnerReleasesFailedWorkForRetry(t *testing.T) {
	item := judgement.WorkItem{RepoID: "repo_a", VersionID: "version_a"}
	source := newPendingSource(item)
	processor := &recordingProcessor{
		source:    source,
		failures:  1,
		completed: make(chan struct{}, 1),
	}
	runner := judgement.NewPendingRunner(
		source,
		judgement.NewMemoryLeases(nil),
		processor,
		judgement.RunnerOptions{Workers: 1, BatchSize: 1, PollInterval: time.Millisecond, LeaseTTL: 100 * time.Millisecond},
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(ctx)
	}()
	select {
	case <-processor.completed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for failed work to retry")
	}
	cancel()
	<-done

	if got := processor.callCount(); got != 2 {
		t.Fatalf("processor calls = %d, want failure then retry", got)
	}
}

func TestPendingRunnerRenewsLeaseToPreventConcurrentProcessing(t *testing.T) {
	item := judgement.WorkItem{RepoID: "repo_a", VersionID: "version_a"}
	source := newPendingSource(item)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	processor := &blockingProcessor{source: source, started: started, release: release}
	leases := judgement.NewMemoryLeases(nil)
	options := judgement.RunnerOptions{Workers: 1, BatchSize: 1, PollInterval: time.Millisecond, LeaseTTL: 30 * time.Millisecond}
	first := judgement.NewPendingRunner(source, leases, processor, options)
	second := judgement.NewPendingRunner(source, leases, processor, options)

	ctx, cancel := context.WithCancel(context.Background())
	var wait sync.WaitGroup
	for _, runner := range []*judgement.PendingRunner{first, second} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			runner.Run(ctx)
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for judgement to start")
	}
	time.Sleep(4 * options.LeaseTTL)
	close(release)
	if !source.waitEmpty(time.Second) {
		t.Fatal("timed out waiting for judgement to complete")
	}
	cancel()
	wait.Wait()

	if got := processor.maxConcurrency(); got > 1 {
		t.Fatalf("maximum processor concurrency = %d, want lease renewal to prevent concurrent execution", got)
	}
}

func TestPendingRunnerDoesNotClaimWorkUntilAWorkerCanProcessIt(t *testing.T) {
	first := judgement.WorkItem{RepoID: "repo_a", VersionID: "version_a"}
	second := judgement.WorkItem{RepoID: "repo_b", VersionID: "version_b"}
	source := newPendingSource(first, second)
	started := make(chan struct{}, 1)
	processor := &blockingProcessor{source: source, started: started, release: make(chan struct{})}
	leases := &observedLeases{
		LeaseStore: judgement.NewMemoryLeases(nil),
		acquired:   make(chan judgement.WorkKey, 2),
	}
	runner := judgement.NewPendingRunner(
		source,
		leases,
		processor,
		judgement.RunnerOptions{Workers: 1, BatchSize: 2, PollInterval: time.Second, LeaseTTL: time.Second},
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(ctx)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first work item")
	}
	select {
	case key := <-leases.acquired:
		if key != (judgement.WorkKey{RepoID: first.RepoID, VersionID: first.VersionID}) {
			t.Fatalf("first lease = %#v, want first work item", key)
		}
	default:
		t.Fatal("first work item was processed without a lease")
	}
	select {
	case key := <-leases.acquired:
		t.Fatalf("queued work was claimed before a worker was available: %#v", key)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	<-done
}

func TestPendingRunnerRestartRediscoversInterruptedPendingWork(t *testing.T) {
	item := judgement.WorkItem{RepoID: "repo_a", VersionID: "version_a"}
	source := newPendingSource(item)
	started := make(chan struct{}, 1)
	firstProcessor := &blockingProcessor{source: source, started: started, release: make(chan struct{})}
	options := judgement.RunnerOptions{Workers: 1, BatchSize: 1, PollInterval: time.Millisecond, LeaseTTL: time.Second}
	first := judgement.NewPendingRunner(source, judgement.NewMemoryLeases(nil), firstProcessor, options)

	firstCtx, stopFirst := context.WithCancel(context.Background())
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		first.Run(firstCtx)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for interrupted judgement")
	}
	stopFirst()
	<-firstDone
	if remaining := source.remaining(); remaining != 1 {
		t.Fatalf("pending work after interrupted runner = %d, want 1", remaining)
	}

	secondProcessor := &recordingProcessor{source: source, completed: make(chan struct{}, 1)}
	second := judgement.NewPendingRunner(source, judgement.NewMemoryLeases(nil), secondProcessor, options)
	secondCtx, stopSecond := context.WithCancel(context.Background())
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		second.Run(secondCtx)
	}()
	select {
	case <-secondProcessor.completed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for restarted runner")
	}
	stopSecond()
	<-secondDone
	if remaining := source.remaining(); remaining != 0 {
		t.Fatalf("pending work after restart = %d, want none", remaining)
	}
}

type pendingSource struct {
	mu    sync.Mutex
	items []judgement.WorkItem
}

func newPendingSource(items ...judgement.WorkItem) *pendingSource {
	return &pendingSource{items: append([]judgement.WorkItem(nil), items...)}
}

func (source *pendingSource) ListPending(_ context.Context, after string, limit int) (judgement.PendingPage, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	start := 0
	if after != "" {
		for index, item := range source.items {
			if itemCursor(item) == after {
				start = index + 1
				break
			}
		}
	}
	end := min(start+limit, len(source.items))
	page := judgement.PendingPage{Items: append([]judgement.WorkItem(nil), source.items[start:end]...)}
	if end < len(source.items) && len(page.Items) > 0 {
		page.NextCursor = itemCursor(page.Items[len(page.Items)-1])
	}
	return page, nil
}

func (source *pendingSource) complete(item judgement.WorkItem) {
	source.mu.Lock()
	defer source.mu.Unlock()
	for index, pending := range source.items {
		if pending == item {
			source.items = append(source.items[:index], source.items[index+1:]...)
			return
		}
	}
}

func (source *pendingSource) remaining() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return len(source.items)
}

func (source *pendingSource) waitEmpty(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if source.remaining() == 0 {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func itemCursor(item judgement.WorkItem) string {
	return item.RepoID + "/" + string(item.VersionID)
}

type recordingProcessor struct {
	mu         sync.Mutex
	source     *pendingSource
	failures   int
	calls      int
	concurrent int
	maximum    int
	completed  chan struct{}
}

func (processor *recordingProcessor) Process(_ context.Context, item judgement.WorkItem) error {
	processor.mu.Lock()
	processor.calls++
	processor.concurrent++
	processor.maximum = max(processor.maximum, processor.concurrent)
	shouldFail := processor.failures > 0
	if shouldFail {
		processor.failures--
	}
	processor.mu.Unlock()

	time.Sleep(2 * time.Millisecond)

	processor.mu.Lock()
	processor.concurrent--
	processor.mu.Unlock()
	if shouldFail {
		return errors.New("temporary judgement failure")
	}
	processor.source.complete(item)
	processor.completed <- struct{}{}
	return nil
}

func (processor *recordingProcessor) callCount() int {
	processor.mu.Lock()
	defer processor.mu.Unlock()
	return processor.calls
}

func (processor *recordingProcessor) maxConcurrency() int {
	processor.mu.Lock()
	defer processor.mu.Unlock()
	return processor.maximum
}

type blockingProcessor struct {
	mu      sync.Mutex
	source  *pendingSource
	started chan struct{}
	release chan struct{}
	calls   int
	active  int
	maximum int
}

type observedLeases struct {
	judgement.LeaseStore
	acquired chan judgement.WorkKey
}

func (leases *observedLeases) Acquire(ctx context.Context, key judgement.WorkKey, owner string, ttl time.Duration) (judgement.Lease, bool, error) {
	lease, acquired, err := leases.LeaseStore.Acquire(ctx, key, owner, ttl)
	if acquired {
		leases.acquired <- key
	}
	return lease, acquired, err
}

func (processor *blockingProcessor) Process(ctx context.Context, item judgement.WorkItem) error {
	processor.mu.Lock()
	processor.calls++
	processor.active++
	processor.maximum = max(processor.maximum, processor.active)
	processor.mu.Unlock()
	defer func() {
		processor.mu.Lock()
		processor.active--
		processor.mu.Unlock()
	}()
	select {
	case processor.started <- struct{}{}:
	default:
	}
	select {
	case <-processor.release:
		processor.source.complete(item)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (processor *blockingProcessor) callCount() int {
	processor.mu.Lock()
	defer processor.mu.Unlock()
	return processor.calls
}

func (processor *blockingProcessor) maxConcurrency() int {
	processor.mu.Lock()
	defer processor.mu.Unlock()
	return processor.maximum
}
