package froggy

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

type ResizingStrategy interface {
	Resize(runningWorkers, minWorkers, maxWorkers int) bool
}

const (
	// defaultIdleTimeout defines the default idle timeout to use when not explicitly specified
	// via the IdleTimeout() option
	defaultIdleTimeout = 5 * time.Second
)

var (
	// ErrSubmitOnStoppedPool is thrown when attempting to submit a task to a pool that has been stopped
	ErrSubmitOnStoppedPool = errors.New("worker pool has been stopped and is no longer accepting tasks")
)

// defaultPanicHandler is the default panic handler
func defaultPanicHandler(panic interface{}) {
	fmt.Printf("Worker exits from a panic: %v\nStack trace: %s\n", panic, string(debug.Stack()))
}

// RunningWorkers returns the current number of running workers
func (p *WorkerPool) RunningWorkers() int {
	return int(atomic.LoadInt32(&p.workerCount))
}

// IdleWorkers returns the current number of idle workers
func (p *WorkerPool) IdleWorkers() int {

	return int(atomic.LoadInt32(&p.idleWorkerCount))
}

// MinWorkers returns the minimum number of worker goroutines
func (p *WorkerPool) MinWorkers() int {
	return p.minWorkers
}

// MaxWorkers returns the maximum number of worker goroutines
func (p *WorkerPool) MaxWorkers() int {
	return p.maxWorkers
}

// MaxCapacity returns the maximum number of tasks that can be waiting in the queue
// at any given time (queue size)
func (p *WorkerPool) MaxCapacity() int {
	return p.maxCapacity
}

// Strategy returns the configured pool resizing strategy
func (p *WorkerPool) Strategy() ResizingStrategy {
	return p.strategy
}

func Context(prtCtx context.Context) Option {
	return func(pool *WorkerPool) {
		pool.context, pool.contextCancel = context.WithCancel(prtCtx)

	}

}

type Option func(pool *WorkerPool)

type WorkerPool struct {
	//	atomic values
	workerCount         int32
	idleWorkerCount     int32
	waitingTaskCount    uint64
	submittedTaskCount  uint64
	successfulTaskCount uint64
	failedTaskCount     uint64
	//	props (must be configured or must not)
	maxWorkers    int
	maxCapacity   int
	minWorkers    int
	idleTimeout   time.Duration
	strategy      ResizingStrategy
	panicHandler  func(interface{})
	context       context.Context
	contextCancel context.CancelFunc
	//	props (private)
	tasks            chan func()
	tasksCloseOnce   sync.Once
	workersWaitGroup sync.WaitGroup
	tasksWaitGroup   sync.WaitGroup
	mutex            sync.Mutex
	stopped          int32
}

func NewPool(maxWorkers int, maxCapacity int, options ...Option) *WorkerPool {

	pool := &WorkerPool{
		maxWorkers:   maxWorkers,
		maxCapacity:  maxCapacity,
		idleTimeout:  defaultIdleTimeout,
		strategy:     Eager(),
		panicHandler: defaultPanicHandler,
	}

	// Apply all options
	for _, opt := range options {
		opt(pool)
	}

	// Make sure options are consistent
	if pool.maxWorkers <= 0 {
		pool.maxWorkers = 1
	}
	if pool.minWorkers > pool.maxWorkers {
		pool.minWorkers = pool.maxWorkers
	}
	if pool.maxCapacity < 0 {
		pool.maxCapacity = 0
	}
	if pool.idleTimeout < 0 {
		pool.idleTimeout = defaultIdleTimeout
	}

	// Initialize base context (if not already set)
	if pool.context == nil {
		Context(context.Background())(pool)
	}

	// Create tasks channel
	pool.tasks = make(chan func(), pool.maxCapacity)

	// Start purger goroutine
	pool.workersWaitGroup.Add(1)
	go pool.purge()

	return
}

func (p *WorkerPool) purge() {
	defer p.workersWaitGroup.Done()

	idleTicker := time.NewTicker(p.idleTimeout)
	defer idleTicker.Stop()

	for {
		select {
		// Timed out waiting for any activity to happen, attempt to stop an idle worker
		case <-idleTicker.C:
			p.maybeStopIdleWorker()
		// Pool context was cancelled, exit
		case <-p.context.Done():
			return
		}
	}
}

// maybeStopIdleWorker attempts to stop an idle worker by sending it a nil task
func (p *WorkerPool) maybeStopIdleWorker() bool {

	if decremented := p.decrementWorkerCount(); !decremented {
		return false
	}

	// Send a nil task to stop an idle worker
	p.tasks <- nil

	return true
}

func (p *WorkerPool) decrementWorkerCount() bool {

	p.mutex.Lock()
	defer p.mutex.Unlock()

	if p.IdleWorkers() <= 0 || p.RunningWorkers() <= p.minWorkers || p.Stopped() {
		return false
	}

	// Decrement worker count
	atomic.AddInt32(&p.workerCount, -1)

	// Decrement idle count
	atomic.AddInt32(&p.idleWorkerCount, -1)

	return true
}
