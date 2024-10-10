package froggy

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
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
	return
}
