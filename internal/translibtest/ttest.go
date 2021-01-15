package translibtest

import (
	"time"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/translib"
	"github.com/prometheus/prometheus/pkg/timestamp"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/number"
	export "go.opentelemetry.io/otel/sdk/export/metric"
	"go.opentelemetry.io/otel/sdk/export/metric/aggregation"
	"go.opentelemetry.io/otel/sdk/resource"
)

func newRecord(
	descriptor *metric.Descriptor,
	labels *label.Set,
	resource *resource.Resource,
	aggregation aggregation.Aggregation,
	start, end time.Time,
) *export.Record {

	r := export.NewRecord(descriptor, labels, resource, aggregation, start, end)
	return &r
}

func KeyValue(k, v string) label.KeyValue {
	return label.Key(k).String(v)
}

func ResourceLabels(kvs ...label.KeyValue) *resource.Resource {
	return resource.NewWithAttributes(kvs...)
}

func Labels(kvs ...label.KeyValue) *label.Set {
	ls := label.NewSet(kvs...)
	return &ls
}

func DoubleCounterPoint(res *resource.Resource, lab *label.Set, name string, start, end time.Time, value float64) *export.Record {
	desc := metric.NewDescriptor(name, metric.SumObserverInstrumentKind, number.Float64Kind)
	return newRecord(&desc, lab, res, translib.Sum(number.NewFloat64Number(value)), start, end)
}

func IntCounterPoint(res *resource.Resource, lab *label.Set, name string, start, end time.Time, value int64) *export.Record {
	desc := metric.NewDescriptor(name, metric.SumObserverInstrumentKind, number.Int64Kind)
	return newRecord(&desc, lab, res, translib.Sum(number.NewInt64Number(value)), start, end)
}

func DoubleGaugePoint(res *resource.Resource, lab *label.Set, name string, end time.Time, value float64) *export.Record {
	desc := metric.NewDescriptor(name, metric.ValueObserverInstrumentKind, number.Float64Kind)
	return newRecord(&desc, lab, res, translib.LastValue{
		V: number.NewFloat64Number(value),
		T: end,
	}, timestamp.Time(0), end)
}

func IntGaugePoint(res *resource.Resource, lab *label.Set, name string, end time.Time, value int64) *export.Record {
	desc := metric.NewDescriptor(name, metric.ValueObserverInstrumentKind, number.Int64Kind)
	return newRecord(&desc, lab, res, translib.LastValue{
		V: number.NewInt64Number(value),
		T: end,
	}, timestamp.Time(0), end)
}

type DoubleHistogramBucketStruct struct {
	Boundary float64
	Count    uint64
}

func DoubleHistogramBucket(boundary float64, count uint64) DoubleHistogramBucketStruct {
	return DoubleHistogramBucketStruct{
		Boundary: boundary,
		Count:    count,
	}
}

func DoubleHistogramPoint(res *resource.Resource, lab *label.Set, name string, start, end time.Time, totalSum float64, totalCount uint64, buckets ...DoubleHistogramBucketStruct) *export.Record {
	blen := 0
	if len(buckets) > 0 {
		blen = len(buckets) - 1
	}
	counts := make([]uint64, len(buckets))
	bounds := make([]float64, blen)
	for i, b := range buckets[:len(bounds)] {
		counts[i] = b.Count
		bounds[i] = b.Boundary
	}
	if len(buckets) > 0 {
		counts[len(buckets)-1] = buckets[len(buckets)-1].Count
	}

	desc := metric.NewDescriptor(name, metric.ValueRecorderInstrumentKind, number.Float64Kind)

	asFloats := make([]float64, len(counts))
	for i := range counts {
		asFloats[i] = float64(counts[i])
	}

	return newRecord(&desc, lab, res, translib.Histogram{
		TotalCount: int64(totalCount),
		TotalSum:   totalSum,
		Counts:     asFloats,
		Boundaries: bounds,
	}, start, end)
}
