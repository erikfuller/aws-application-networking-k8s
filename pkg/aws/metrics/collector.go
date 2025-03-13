package metrics

import (
	"context"
	"time"

	"github.com/aws/smithy-go/metrics"
	"github.com/aws/smithy-go/middleware"
	"github.com/prometheus/client_golang/prometheus"
)

type collector struct {
	instruments *instruments
}

func NewCollector(registerer prometheus.Registerer) (*collector, error) {
	instruments, err := newInstruments(registerer)
	if err != nil {
		return nil, err
	}
	return &collector{
		instruments: instruments,
	}, nil
}

// MetricsMiddleware returns a middleware that collects metrics for AWS SDK v2 operations
func (c *collector) MetricsMiddleware() middleware.Stack {
	return middleware.Stack{
		Deserialize: middleware.DeserializeMiddleware{
			Name: "MetricsCollector",
			Next: middleware.DeserializeHandlerFunc(
				func(ctx context.Context, in middleware.DeserializeInput) (
					out middleware.DeserializeOutput, metadata middleware.Metadata, err error) {
					start := time.Now()
					out, metadata, err = in.Handler.Handle(ctx, in)

					// Get operation info from context
					operation := middleware.GetOperationName(ctx)
					service := middleware.GetServiceID(ctx)

					// Record metrics for the API call
					c.recordAPIMetrics(service, operation, err, time.Since(start))

					return out, metadata, err
				},
			),
		},
	}
}

func (c *collector) recordAPIMetrics(service, operation string, err error, duration time.Duration) {
	statusCode := "0"
	errorCode := ""

	// Extract status code and error code
	if err != nil {
		if apiErr, ok := err.(interface {
			ErrorCode() string
		}); ok {
			errorCode = apiErr.ErrorCode()
		} else {
			errorCode = "internal"
		}
	}

	// Record total API calls
	c.instruments.apiCallsTotal.With(map[string]string{
		labelService:    service,
		labelOperation:  operation,
		labelStatusCode: statusCode,
		labelErrorCode:  errorCode,
	}).Inc()

	// Record API call duration
	c.instruments.apiCallDurationSeconds.With(map[string]string{
		labelService:   service,
		labelOperation: operation,
	}).Observe(duration.Seconds())
}

// Implement metrics.Provider interface
type metricsProvider struct {
	collector *collector
}

func NewMetricsProvider(collector *collector) metrics.Provider {
	return &metricsProvider{collector: collector}
}

func (p *metricsProvider) Metrics(ctx context.Context) (metrics.Metrics, error) {
	// Create a new metrics recorder
	recorder := &metricsRecorder{
		collector: p.collector,
		started:   time.Now(),
	}
	return recorder, nil
}

// metricsRecorder implements metrics.Metrics interface
type metricsRecorder struct {
	collector *collector
	started   time.Time
}

func (r *metricsRecorder) Close() error {
	return nil
}

// Usage example:
/*
func configureSDKClient(cfg *aws.Config) {
    collector, err := NewCollector(prometheus.DefaultRegisterer)
    if err != nil {
        // Handle error
    }

    // Add metrics middleware to the config
    cfg.APIOptions = append(cfg.APIOptions,
        func(stack *middleware.Stack) error {
            return stack.Merge(collector.MetricsMiddleware())
        },
    )
}
*/
