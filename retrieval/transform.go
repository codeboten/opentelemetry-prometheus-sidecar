/*
Copyright 2018 Google Inc.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package retrieval

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/pkg/timestamp"
	"github.com/prometheus/prometheus/tsdb/record"
	otelapi "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/number"
	export "go.opentelemetry.io/otel/sdk/export/metric"
	"go.opentelemetry.io/otel/sdk/export/metric/aggregation"
	otelagg "go.opentelemetry.io/otel/sdk/export/metric/aggregation"
	"go.opentelemetry.io/otel/sdk/resource"
)

// Appender appends a time series with exactly one data point
// The client may cache the computed hash more easily, which is why its part of the call
// and not done by the Appender's implementation.
type Appender interface {
	Append(export.Record) error
}

type sampleBuilder struct {
	series      seriesGetter
	maxPointAge time.Duration
}

// next extracts the next sample from the TSDB input sample list and returns
// the remainder of the input.
//
// Note in cases when no timeseries point is produced, the return value has a
// nil timeseries and a nil error.  These are observable as the difference between
// "processed" and "produced" in the calling code (see manager.go).  TODO: Add
// a label to identify each of the paths below.
func (b *sampleBuilder) next(ctx context.Context, samples []record.RefSample) (*export.Record, []record.RefSample, error) {
	sample := samples[0]
	tailSamples := samples[1:]

	if math.IsNaN(sample.V) {
		fmt.Println("NAN CASE") // TODO: Metric this case?
		return nil, tailSamples, nil
	}

	entry, ok, err := b.series.get(ctx, walRef(sample.Ref))
	if err != nil {
		return nil, samples, errors.Wrap(err, "get series information")
	}
	if !ok {
		fmt.Println("!OK CASE") // TODO: Log this case?
		return nil, tailSamples, nil
	}

	if !entry.exported {
		fmt.Println("!EXPORTED CASE") // TODO: Debug-log this case?
		return nil, tailSamples, nil
	}

	var (
		ikind     otelapi.InstrumentKind = -1 // Not used in all cases, indicates monotonic sum in some cases.
		nkind     number.Kind            = -1
		agg       otelagg.Aggregation
		startTime promTime = 0
		endTime   promTime = promTime(sample.T)
	)

	if entry.metadata.ValueType == metadata.INT64 {
		nkind = number.Int64Kind
	} else {
		nkind = number.Float64Kind
	}

	switch entry.metadata.MetricType {
	case textparse.MetricTypeCounter:
		ikind = otelapi.CounterInstrumentKind

		var value float64
		startTime, value, ok = b.series.getResetAdjusted(walRef(sample.Ref), endTime, sample.V)
		if !ok {
			return nil, tailSamples, nil
		}

		if entry.metadata.ValueType == metadata.INT64 {
			agg = numberSum(number.NewInt64Number(int64(value)))
		} else {
			agg = numberSum(number.NewFloat64Number(value))
		}

	case textparse.MetricTypeGauge, textparse.MetricTypeUnknown:
		ikind = otelapi.ValueRecorderInstrumentKind

		if entry.metadata.ValueType == metadata.INT64 {
			agg = numberLastValue{
				V: number.NewInt64Number(int64(sample.V)),
				T: timestamp.Time(sample.T),
			}
		} else {
			agg = numberLastValue{
				V: number.NewFloat64Number(sample.V),
				T: timestamp.Time(sample.T),
			}
		}

	case textparse.MetricTypeSummary:
		nkind = number.Float64Kind

		switch entry.suffix {
		case metricSuffixSum:
			ikind = otelapi.CounterInstrumentKind

			var value float64
			startTime, value, ok = b.series.getResetAdjusted(walRef(sample.Ref), endTime, sample.V)
			if !ok {
				return nil, tailSamples, nil
			}
			agg = numberSum(number.NewFloat64Number(value))
		case metricSuffixCount:
			ikind = otelapi.CounterInstrumentKind

			var value float64
			startTime, value, ok = b.series.getResetAdjusted(walRef(sample.Ref), endTime, sample.V)
			if !ok {
				return nil, tailSamples, nil
			}
			agg = numberSum(number.NewFloat64Number(value))
		case "": // Actual quantiles.
			ikind = otelapi.ValueObserverInstrumentKind

			agg = numberSum(number.NewFloat64Number(sample.V))
		default:
			return nil, tailSamples, errors.Errorf("unexpected metric name suffix %q", entry.suffix)
		}

	case textparse.MetricTypeHistogram:
		// We pass in the original lset for matching since Prometheus's target label must
		// be the same as well.
		// Note: Always using DoubleHistogram points, ignores entry.metadata.ValueType.
		agg, startTime, tailSamples, err = b.buildHistogram(ctx, entry.metadata.Metric, entry.lset, samples)
		if agg == nil || err != nil {
			if agg == nil && err == nil {
				err = errors.Errorf("missing histogram points")
			}
			return nil, tailSamples, err
		}
		ikind = otelapi.ValueRecorderInstrumentKind

	default:
		return nil, samples[1:], errors.Errorf("unexpected metric type %s", entry.metadata.MetricType)
	}

	if !b.series.updateSampleInterval(entry.key(), startTime, endTime) {
		return nil, tailSamples, nil
	}
	if b.maxPointAge > 0 {
		when := time.Unix(sample.T/1000, int64(time.Duration(sample.T%1000)*time.Millisecond))
		if time.Since(when) > b.maxPointAge {
			return nil, tailSamples, nil
		}
	}

	exportDesc := otelapi.NewDescriptor(entry.desc.Name, ikind, nkind)

	// Note: This ToSlice() is an unnecessary copy. TODO: Expose a
	// *label.Set constructor in the SDK upstream?
	res := resource.NewWithAttributes(entry.desc.Resource.ToSlice()...)

	ts := export.NewRecord(&exportDesc, entry.desc.Labels, res, agg, startTime.Time(), endTime.Time())

	return &ts, tailSamples, nil
}

const (
	metricSuffixBucket = "_bucket"
	metricSuffixSum    = "_sum"
	metricSuffixCount  = "_count"
	metricSuffixTotal  = "_total"
)

func stripComplexMetricSuffix(name string) (prefix string, suffix string, ok bool) {
	if strings.HasSuffix(name, metricSuffixBucket) {
		return name[:len(name)-len(metricSuffixBucket)], metricSuffixBucket, true
	}
	if strings.HasSuffix(name, metricSuffixCount) {
		return name[:len(name)-len(metricSuffixCount)], metricSuffixCount, true
	}
	if strings.HasSuffix(name, metricSuffixSum) {
		return name[:len(name)-len(metricSuffixSum)], metricSuffixSum, true
	}
	if strings.HasSuffix(name, metricSuffixTotal) {
		return name[:len(name)-len(metricSuffixTotal)], metricSuffixTotal, true
	}
	return name, "", false
}

func getMetricName(prefix string, promName string) string {
	if prefix == "" {
		return promName
	}
	return prefix + promName
}

type distribution struct {
	bounds []float64
	values []uint64
}

func (d *distribution) Len() int {
	return len(d.bounds)
}

func (d *distribution) Less(i, j int) bool {
	return d.bounds[i] < d.bounds[j]
}

func (d *distribution) Swap(i, j int) {
	d.bounds[i], d.bounds[j] = d.bounds[j], d.bounds[i]
	d.values[i], d.values[j] = d.values[j], d.values[i]
}

// buildHistogram consumes series from the beginning of the input slice that belong to a histogram
// with the given metric name and label set.
// It returns the reset timestamp along with the distrubution.
func (b *sampleBuilder) buildHistogram(
	ctx context.Context,
	baseName string,
	matchLset labels.Labels,
	samples []record.RefSample,
) (aggregation.Histogram, promTime, []record.RefSample, error) {
	var (
		consumed       int
		count, sum     float64
		resetTimestamp promTime
		lastTimestamp  promTime
		dist           = distribution{bounds: make([]float64, 0, 20), values: make([]uint64, 0, 20)}
		skip           = false
	)
	// We assume that all series belonging to the histogram are sequential. Consume series
	// until we hit a new metric.
Loop:
	for i, s := range samples {
		e, ok, err := b.series.get(ctx, walRef(s.Ref))
		if err != nil {
			return nil, 0, samples, err
		}
		if !ok {
			consumed++
			// TODO(fabxc): increment metric.
			continue
		}
		name := e.lset.Get("__name__")
		// The series matches if it has the same base name, the remainder is a valid histogram suffix,
		// and the labels aside from the le and __name__ label match up.
		if !strings.HasPrefix(name, baseName) || !histogramLabelsEqual(e.lset, matchLset) {
			break
		}
		// In general, a scrape cannot contain the same (set of) series repeatedlty but for different timestamps.
		// It could still happen with bad clients though and we are doing it in tests for simplicity.
		// If we detect the same series as before but for a different timestamp, return the histogram up to this
		// series and leave the duplicate time series untouched on the input.
		sT := promTime(s.T)
		if i > 0 && sT != lastTimestamp {
			// TODO: counter
			break
		}
		lastTimestamp = sT

		rt, v, ok := b.series.getResetAdjusted(walRef(s.Ref), sT, s.V)

		switch name[len(baseName):] {
		case metricSuffixSum:
			sum = v
		case metricSuffixCount:
			count = v
			// We take the count series as the authoritative source for the overall reset timestamp.
			resetTimestamp = rt
		case metricSuffixBucket:
			upper, err := strconv.ParseFloat(e.lset.Get("le"), 64)

			if err != nil {
				consumed++
				// TODO: increment metric.
				continue
			}
			dist.bounds = append(dist.bounds, upper)
			dist.values = append(dist.values, uint64(v))
		default:
			break Loop
		}
		// If a series appeared for the first time, we won't get a valid reset timestamp yet.
		// This may happen if the histogram is entirely new or if new series appeared through bucket changes.
		// We skip the entire histogram sample in this case.
		if !ok {
			skip = true
		}
		consumed++
	}
	// Don't emit a sample if we explicitly skip it or no reset timestamp was set because the
	// count series was missing.
	if skip || resetTimestamp == 0 {
		// TODO add a counter for this event. Note there is
		// more validation we could do: the sum should agree
		// with the buckets.
		return nil, 0, samples[consumed:], nil
	}
	// We do not assume that the buckets in the sample batch are in order, so we sort them again here.
	// The code below relies on this to convert between Prometheus's and the output's bucketing approaches.
	sort.Sort(&dist)
	// Reuse slices we already populated to build final bounds and values.
	var (
		values  = dist.values[:0]
		bounds  = dist.bounds[:0]
		prevVal uint64
	)
	// Note: dist.bounds and dist.values have the same size.
	for i := range dist.bounds {
		val := dist.values[i] - prevVal
		prevVal = dist.values[i]
		values = append(values, val)
	}

	if len(dist.bounds) > 0 {
		bounds = dist.bounds[:len(dist.bounds)-1]
	}
	// Note: The []uint64 to []float64 and uint64 to int64 below
	// will disappear in OTel-Go v0.16.0.
	asFloats := make([]float64, len(values))
	for i := range values {
		asFloats[i] = float64(values[i])
	}
	histogram := float64Histogram{
		sum:    sum,
		count:  int64(count),
		bounds: bounds,
		values: asFloats,
	}
	return histogram, resetTimestamp, samples[consumed:], nil
}

// histogramLabelsEqual checks whether two label sets for a histogram series are equal aside from their
// le and __name__ labels.
func histogramLabelsEqual(a, b labels.Labels) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].Name == "le" || a[i].Name == "__name__" {
			i++
			continue
		}
		if b[j].Name == "le" || b[j].Name == "__name__" {
			j++
			continue
		}
		if a[i] != b[j] {
			return false
		}
		i++
		j++
	}
	// Consume trailing le and __name__ labels so the check below passes correctly.
	for i < len(a) {
		if a[i].Name == "le" || a[i].Name == "__name__" {
			i++
			continue
		}
		break
	}
	for j < len(b) {
		if b[j].Name == "le" || b[j].Name == "__name__" {
			j++
			continue
		}
		break
	}
	// If one label set still has labels left, they are not equal.
	return i == len(a) && j == len(b)
}

type numberSum number.Number

var _ otelagg.Sum = numberSum(0)

func (s numberSum) Kind() otelagg.Kind {
	return otelagg.SumKind
}

func (s numberSum) Sum() (number.Number, error) {
	return number.Number(s), nil
}

type numberLastValue struct {
	V number.Number
	T time.Time
}

var _ otelagg.LastValue = numberLastValue{}

func (lv numberLastValue) Kind() otelagg.Kind {
	return otelagg.LastValueKind
}

func (lv numberLastValue) LastValue() (number.Number, time.Time, error) {
	return lv.V, lv.T, nil
}

type float64Histogram struct {
	count  int64
	sum    float64
	values []float64
	bounds []float64
}

var _ otelagg.Histogram = float64Histogram{}

func (h float64Histogram) Kind() otelagg.Kind {
	return otelagg.HistogramKind
}

func (h float64Histogram) Count() (int64, error) {
	return h.count, nil
}

func (h float64Histogram) Sum() (number.Number, error) {
	return number.NewFloat64Number(h.sum), nil
}

func (h float64Histogram) Histogram() (aggregation.Buckets, error) {
	return aggregation.Buckets{
		Boundaries: h.bounds,
		Counts:     h.values,
	}, nil
}
