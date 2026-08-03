package usage

import (
	"log/slog"
	"sync"
	"time"
)

// UsageLogger provides asynchronous, non-blocking logging of usage records to SQLite.
// Records are buffered in a channel and flushed in batches either when the batch reaches
// BatchSize or every FlushInterval, whichever comes first. On shutdown it performs a final drain.
type UsageLogger struct {
	store   *Store
	ch      chan UsageRecord   // Buffered channel; capacity configurable (default 1024)
	batchCh chan []UsageRecord // Internal batching channel to decouple flush logic

	batchSize     int
	flushInterval time.Duration

	stopCh    chan struct{}
	doneCh    chan struct{}
	closeOnce sync.Once

	log *slog.Logger
}

// LoggerOptions configures the async usage logger behavior.
type LoggerOptions struct {
	BufferSize    int           // Channel buffer size (default 1024)
	BatchSize     int           // Records per batch insert (default 50)
	FlushInterval time.Duration // Max time between flushes (default 1s)
}

// DefaultLoggerOptions returns sensible defaults matching the spec.
func DefaultLoggerOptions() LoggerOptions {
	return LoggerOptions{
		BufferSize:    1024,
		BatchSize:     50,
		FlushInterval: time.Second,
	}
}

// NewUsageLogger creates and starts an async usage logger backed by the given store.
func NewUsageLogger(store *Store, opts LoggerOptions) *UsageLogger {
	if opts.BufferSize <= 0 {
		opts.BufferSize = DefaultLoggerOptions().BufferSize
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultLoggerOptions().BatchSize
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = DefaultLoggerOptions().FlushInterval
	}

	logger := &UsageLogger{
		store:         store,
		ch:            make(chan UsageRecord, opts.BufferSize),
		batchCh:       make(chan []UsageRecord, 16),
		batchSize:     opts.BatchSize,
		flushInterval: opts.FlushInterval,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
		log:           slog.Default(),
	}

	go logger.run()
	return logger
}

// Log enqueues a usage record for async persistence. Non-blocking when buffer has space;
// drops the oldest entry if full to avoid blocking the proxy request path.
func (ul *UsageLogger) Log(record UsageRecord) {
	select {
	case ul.ch <- record:
		// queued successfully
	default:
		// Channel full — drop oldest and queue new one, or give up entirely.
		ul.log.Warn("usage logger channel full; dropping oldest record")
		select {
		case <-ul.ch: // discard oldest
			select {
			case ul.ch <- record:
				return // queued after making space
			default:
				return // still no room, give up to avoid blocking proxy path
			}
		default:
			return // give up entirely to avoid blocking proxy path
		}
	}
}

// run is the background goroutine that batches records and flushes them periodically.
func (ul *UsageLogger) run() {
	defer close(ul.doneCh)

	ticker := time.NewTicker(ul.flushInterval)
	defer ticker.Stop()

	var batch []UsageRecord
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := ul.store.BatchInsert(batch); err != nil {
			ul.log.Error("batch insert failed", "error", err, "records_dropped", len(batch))
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ul.stopCh:
			flush()
			for {
				select {
				case rec := <-ul.ch:
					batch = append(batch, rec)
					if len(batch) >= ul.batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case rec := <-ul.ch:
			batch = append(batch, rec)
			if len(batch) >= ul.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// Shutdown signals the logger to flush any remaining records and stop. Call during graceful shutdown.
func (ul *UsageLogger) Shutdown(ctxDone <-chan struct{}) {
	ul.closeOnce.Do(func() {
		close(ul.stopCh)
	})

	select {
	case <-ul.doneCh:
	case <-ctxDone:
	}
}
