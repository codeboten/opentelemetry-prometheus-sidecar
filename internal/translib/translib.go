package translib

import (
	"time"

	"go.opentelemetry.io/otel/metric/number"
	"go.opentelemetry.io/otel/sdk/export/metric/aggregation"
)

type Sum number.Number

var _ aggregation.Sum = Sum(0)

func (s Sum) Kind() aggregation.Kind {
	return aggregation.SumKind
}

func (s Sum) Sum() (number.Number, error) {
	return number.Number(s), nil
}

type LastValue struct {
	V number.Number
	T time.Time
}

var _ aggregation.LastValue = LastValue{}

func (lv LastValue) Kind() aggregation.Kind {
	return aggregation.LastValueKind
}

func (lv LastValue) LastValue() (number.Number, time.Time, error) {
	return lv.V, lv.T, nil
}

type Histogram struct {
	TotalCount int64
	TotalSum   float64
	Counts     []float64
	Boundaries []float64
}

var _ aggregation.Histogram = Histogram{}

func (h Histogram) Kind() aggregation.Kind {
	return aggregation.HistogramKind
}

func (h Histogram) Count() (int64, error) {
	return h.TotalCount, nil
}

func (h Histogram) Sum() (number.Number, error) {
	return number.NewFloat64Number(h.TotalSum), nil
}

func (h Histogram) Histogram() (aggregation.Buckets, error) {
	return aggregation.Buckets{
		Boundaries: h.Boundaries,
		Counts:     h.Counts,
	}, nil
}
