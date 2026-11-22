package resources

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// RegisterMetrics reports the memory sandboxes hold against their limits,
// summed per runtime from each live run's latest sample: what capacity
// planning under a VM runtime needs (a Kata guest keeps what it touched).
// Read at collection time; nothing is recorded on the sampling path.
func (s *Sampler) RegisterMetrics(meter metric.Meter) error {
	used, err1 := meter.Int64ObservableGauge("apothecary.sandboxes.memory.used",
		metric.WithUnit("By"), metric.WithDescription("working-set memory of live sandboxes, by runtime"))
	limit, err2 := meter.Int64ObservableGauge("apothecary.sandboxes.memory.limit",
		metric.WithUnit("By"), metric.WithDescription("memory limits of live sandboxes, by runtime (the node's memory when uncapped)"))
	if err := errors.Join(err1, err2); err != nil {
		return err
	}
	_, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for rt, m := range s.memoryByRuntime() {
			attrs := metric.WithAttributes(attribute.String("runtime", rt))
			o.ObserveInt64(used, m.used, attrs)
			o.ObserveInt64(limit, m.limit, attrs)
		}
		return nil
	}, used, limit)
	return err
}

type memory struct{ used, limit int64 }

// memoryByRuntime sums each live run's latest sample per runtime ("default":
// a provisioner that offers no choice).
func (s *Sampler) memoryByRuntime() map[string]memory {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]memory{}
	for id, buf := range s.buf {
		if len(buf) == 0 {
			continue
		}
		rt := string(s.runtime[id])
		if rt == "" {
			rt = "default"
		}
		last := buf[len(buf)-1]
		m := out[rt]
		m.used += last.MemUsed
		m.limit += last.MemLimit
		out[rt] = m
	}
	return out
}
