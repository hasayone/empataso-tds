package store

import "time"

// confirmedSeriesSQL is the confirmed conversion buckets for the dashboard chart.
// Confirmed means lead and sale. FINAL collapses an unmerged repeat of the same click and status.
// The old postbacks table is not included: it has no status.
const confirmedSeriesSQL = `SELECT toStartOfInterval(ts, INTERVAL ? second) AS t,
        count() AS conv, sum(profit) AS revenue
 FROM conversions FINAL
 WHERE ts >= ? AND ts < ? AND status IN ('lead', 'sale')
 GROUP BY t`

// confirmedSeriesFilteredSQL is confirmedSeriesSQL restricted to clicks in eventsWhere.
// eventsWhere is empty or a fragment from buildWhere that starts with " AND ".
// Placeholders are step, from, to, then from, to and the where args for the cid subquery.
func confirmedSeriesFilteredSQL(eventsWhere string) string {
	return `SELECT toStartOfInterval(ts, INTERVAL ? second) AS t,
        count() AS conv, sum(profit) AS revenue
 FROM conversions FINAL
 WHERE ts >= ? AND ts < ? AND status IN ('lead', 'sale')` + eventsCidInSQL(eventsWhere) + `
 GROUP BY t`
}

// seriesHit is one bucket before empty gaps are filled.
type seriesHit struct {
	total, unique, bots int64
	conv                int64
	revenue             float64
}

// capSeriesStep raises stepSec so [from, to) has at most 400 buckets.
// A step below 3600 becomes 3600. from and to are the report window.
// It returns the step in seconds.
func capSeriesStep(from, to time.Time, stepSec int) int {
	const maxPoints = 400
	const minStep = 3600
	if stepSec < minStep {
		stepSec = minStep
	}
	if !to.After(from) {
		return stepSec
	}
	span := int(to.Sub(from).Seconds())
	if span < 1 {
		return stepSec
	}
	for span/stepSec+2 > maxPoints {
		next := span / (maxPoints - 1)
		if next <= stepSec {
			next = stepSec + minStep
		}
		stepSec = next
	}
	return stepSec
}

// bucketStart is the UTC start of the step that contains t.
// ClickHouse toStartOfInterval aligns to the unix epoch the same way.
// stepSec is the bucket length. It returns the bucket start.
func bucketStart(t time.Time, stepSec int) time.Time {
	if stepSec < 1 {
		stepSec = 3600
	}
	sec := t.Unix()
	step := int64(stepSec)
	sec -= sec % step
	return time.Unix(sec, 0).UTC()
}

// fillSeries builds one point per bucket from the first interval that overlaps from until to.
// Missing buckets are zeros, so the chart axis does not skip a quiet hour.
// A bucket that has conversions and no clicks is kept. It returns at most 400 points.
func fillSeries(from, to time.Time, stepSec int, hits map[int64]seriesHit) []TSPoint {
	if stepSec < 3600 {
		stepSec = 3600
	}
	if !to.After(from) {
		return []TSPoint{}
	}
	out := make([]TSPoint, 0, 32)
	for t := bucketStart(from, stepSec); t.Before(to); t = t.Add(time.Duration(stepSec) * time.Second) {
		h := hits[t.Unix()]
		out = append(out, TSPoint{
			TS: t, Total: h.total, Unique: h.unique, Bots: h.bots,
			Conv: h.conv, Revenue: h.revenue,
		})
		if len(out) >= 400 {
			break
		}
	}
	return out
}
