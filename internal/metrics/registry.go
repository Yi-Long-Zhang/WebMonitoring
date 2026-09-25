package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"webmonitoring/internal/security"
)

type Registry struct {
	mu         sync.RWMutex
	counters   map[string]float64
	gauges     map[string]float64
	histograms map[string]*histogram
	collectors []func(*Registry)
}

type histogram struct {
	buckets []float64
	counts  []uint64
	count   uint64
	sum     float64
}

func New() *Registry {
	return &Registry{
		counters:   make(map[string]float64),
		gauges:     make(map[string]float64),
		histograms: make(map[string]*histogram),
	}
}

func (r *Registry) Inc(name string, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[key(name, labels)]++
}

func (r *Registry) Add(name string, value float64, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[key(name, labels)] += value
}

func (r *Registry) Set(name string, value float64, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges[key(name, labels)] = value
}

func (r *Registry) ObserveDuration(name string, duration time.Duration, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	metricKey := key(name, labels)
	item := r.histograms[metricKey]
	if item == nil {
		item = &histogram{
			buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			counts:  make([]uint64, 11),
		}
		r.histograms[metricKey] = item
	}
	value := duration.Seconds()
	item.count++
	item.sum += value
	for index, bucket := range item.buckets {
		if value <= bucket {
			item.counts[index]++
		}
	}
}

// OnCollect registers a function that is run before each scrape, so a value that
// is cheaper to read on demand than to keep fresh -- the connection pool's
// counters -- is current at the moment it is reported instead of lagging behind
// a background sampler. A collector must not block; it runs on the scrape path.
func (r *Registry) OnCollect(collect func(*Registry)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.collectors = append(r.collectors, collect)
}

func (r *Registry) collect() {
	r.mu.RLock()
	collectors := make([]func(*Registry), len(r.collectors))
	copy(collectors, r.collectors)
	r.mu.RUnlock()
	// Run outside the lock: a collector calls Set, which takes it.
	for _, collect := range collectors {
		collect(r)
	}
}

func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		r.collect()
		r.mu.RLock()
		defer r.mu.RUnlock()
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var builder strings.Builder
		counterKeys := sortedKeys(r.counters)
		for _, metricKey := range counterKeys {
			name, labels := splitKey(metricKey)
			fmt.Fprintf(&builder, "%s%s %s\n", name, labels, strconv.FormatFloat(r.counters[metricKey], 'f', -1, 64))
		}
		gaugeKeys := sortedKeys(r.gauges)
		for _, metricKey := range gaugeKeys {
			name, labels := splitKey(metricKey)
			fmt.Fprintf(&builder, "%s%s %s\n", name, labels, strconv.FormatFloat(r.gauges[metricKey], 'f', -1, 64))
		}
		histogramKeys := sortedHistogramKeys(r.histograms)
		for _, metricKey := range histogramKeys {
			name, labels := splitKey(metricKey)
			item := r.histograms[metricKey]
			for index, bucket := range item.buckets {
				fmt.Fprintf(&builder, "%s_bucket%s %s\n", name, addLabel(labels, "le", strconv.FormatFloat(bucket, 'f', -1, 64)), strconv.FormatUint(item.counts[index], 10))
			}
			fmt.Fprintf(&builder, "%s_bucket%s %s\n", name, addLabel(labels, "le", "+Inf"), strconv.FormatUint(item.count, 10))
			fmt.Fprintf(&builder, "%s_sum%s %s\n", name, labels, strconv.FormatFloat(item.sum, 'f', -1, 64))
			fmt.Fprintf(&builder, "%s_count%s %s\n", name, labels, strconv.FormatUint(item.count, 10))
		}
		_, _ = writer.Write([]byte(builder.String()))
	})
}

func key(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	names := make([]string, 0, len(labels))
	for label := range labels {
		names = append(names, label)
	}
	sort.Strings(names)
	var builder strings.Builder
	builder.WriteString(name)
	builder.WriteByte('{')
	for index, label := range names {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(label)
		builder.WriteString(`="`)
		value := sanitizeLabel(label, labels[label])
		builder.WriteString(escape(value))
		builder.WriteByte('"')
	}
	builder.WriteByte('}')
	return builder.String()
}

func sanitizeLabel(name, value string) string {
	if security.IsSensitiveName(name) {
		return "[REDACTED]"
	}
	return security.RedactString(value)
}

func splitKey(value string) (string, string) {
	index := strings.IndexByte(value, '{')
	if index < 0 {
		return value, ""
	}
	return value[:index], value[index:]
}

func addLabel(labelSet, name, value string) string {
	if labelSet == "" {
		return `{` + name + `="` + escape(value) + `"}`
	}
	return labelSet[:len(labelSet)-1] + `,` + name + `="` + escape(value) + `"}`
}

func escape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return strings.ReplaceAll(value, "\n", `\n`)
}

func sortedKeys(values map[string]float64) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sortedHistogramKeys(values map[string]*histogram) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (r *Registry) Snapshot() string {
	recorder := httptestRecorder{}
	r.Handler().ServeHTTP(&recorder, nil)
	return recorder.body.String()
}

type httptestRecorder struct {
	body strings.Builder
}

func (r *httptestRecorder) Header() http.Header { return make(http.Header) }
func (r *httptestRecorder) WriteHeader(int)     {}
func (r *httptestRecorder) Write(value []byte) (int, error) {
	return r.body.Write(value)
}
