package froggy

import (
	"context"
	"sync"
	"time"
)

type ResizingStrategy interface {
	Resize(runningWorkers, minWorkers, maxWorkers int) bool
}

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
