package usageanalytics

// latencyBucketBounds are upper bounds in milliseconds. The final bucket is
// catch-all for values above the last bound.
var latencyBucketBounds = [...]int64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000}

const latencyBucketCount = len(latencyBucketBounds) + 1

type latencyHistogram [latencyBucketCount]int64

func latencyBucketIndex(ms int64) int {
	if ms <= 0 {
		return -1
	}
	for index, bound := range latencyBucketBounds {
		if ms <= bound {
			return index
		}
	}
	return len(latencyBucketBounds)
}

func (h *latencyHistogram) observe(ms int64) {
	index := latencyBucketIndex(ms)
	if index >= 0 {
		h[index]++
	}
}

func (h latencyHistogram) total() int64 {
	var total int64
	for _, count := range h {
		total += count
	}
	return total
}

func (h latencyHistogram) p95() float64 {
	total := h.total()
	if total <= 0 {
		return 0
	}
	target := (total*95 + 99) / 100
	var cumulative int64
	for index, count := range h {
		cumulative += count
		if cumulative >= target {
			if index < len(latencyBucketBounds) {
				return float64(latencyBucketBounds[index])
			}
			return float64(latencyBucketBounds[len(latencyBucketBounds)-1])
		}
	}
	return float64(latencyBucketBounds[len(latencyBucketBounds)-1])
}

func mergeLatencyHistogram(left, right latencyHistogram) latencyHistogram {
	for index := range left {
		left[index] += right[index]
	}
	return left
}
