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

// SubmittedTasks returns the total number of tasks submitted since the pool was created
func (p *WorkerPool) SubmittedTasks() uint64 {
	return atomic.LoadUint64(&p.submittedTaskCount)
}

// WaitingTasks returns the current number of tasks in the queue that are waiting to be executed
func (p *WorkerPool) WaitingTasks() uint64 {
	return atomic.LoadUint64(&p.waitingTaskCount)
}

// SuccessfulTasks returns the total number of tasks that have successfully completed their exection
// since the pool was created
func (p *WorkerPool) SuccessfulTasks() uint64 {
	return atomic.LoadUint64(&p.successfulTaskCount)
}

// FailedTasks returns the total number of tasks that completed with panic since the pool was created
func (p *WorkerPool) FailedTasks() uint64 {
	return atomic.LoadUint64(&p.failedTaskCount)
}

// CompletedTasks returns the total number of tasks that have completed their exection either successfully
// or with panic since the pool was created
func (p *WorkerPool) CompletedTasks() uint64 {
	return p.SuccessfulTasks() + p.FailedTasks()
}

// Stopped returns true if the pool has been stopped and is no longer accepting tasks, and false otherwise.
func (p *WorkerPool) Stopped() bool {
	return atomic.LoadInt32(&p.stopped) == 1
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

	if pool.minWorkers > 0 {
		for i := 0; i < pool.minWorkers; i++ {
			pool.maybeStartWorker(nil)
		}
	}
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
func (p *WorkerPool) maybeStartWorker(firstTask func()) bool {

	if incremented := p.incrementWorkerCount(); !incremented {
		return false
	}

	if firstTask == nil {
		// Worker starts idle
		atomic.AddInt32(&p.idleWorkerCount, 1)
	}

	// Launch worker goroutine
	go worker(p.context, &p.workersWaitGroup, firstTask, p.tasks, p.executeTask, &p.tasksWaitGroup)

	return true
}

func (p *WorkerPool) executeTask(task func(), isFirstTask bool) {

	defer func() {
		if panic := recover(); panic != nil {
			// Increment failed task count
			atomic.AddUint64(&p.failedTaskCount, 1)

			// Invoke panic handler
			p.panicHandler(panic)

			// Increment idle count
			atomic.AddInt32(&p.idleWorkerCount, 1)
		}
		p.tasksWaitGroup.Done()
	}()

	// Decrement idle count
	if !isFirstTask {
		atomic.AddInt32(&p.idleWorkerCount, -1)
	}

	// Decrement waiting task count
	atomic.AddUint64(&p.waitingTaskCount, ^uint64(0))

	// Execute task
	task()

	// Increment successful task count
	atomic.AddUint64(&p.successfulTaskCount, 1)

	// Increment idle count
	atomic.AddInt32(&p.idleWorkerCount, 1)
}

func (p *WorkerPool) incrementWorkerCount() bool {

	p.mutex.Lock()
	defer p.mutex.Unlock()

	runningWorkerCount := p.RunningWorkers()

	// Reached max workers, do not create a new one
	if runningWorkerCount >= p.maxWorkers {
		return false
	}

	// Idle workers available, do not create a new one
	if runningWorkerCount >= p.minWorkers && runningWorkerCount > 0 && p.IdleWorkers() > 0 {
		return false
	}

	// Execute the resizing strategy to determine if we should create more workers
	if resize := p.strategy.Resize(runningWorkerCount, p.minWorkers, p.maxWorkers); !resize {
		return false
	}

	// Increment worker count
	atomic.AddInt32(&p.workerCount, 1)

	// Increment wait group
	p.workersWaitGroup.Add(1)

	return true
}
