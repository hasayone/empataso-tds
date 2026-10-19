package store

import (
	"log"
	"time"
)

// originWaveProbeInterval is how long an engine waits before re-reading
// system.columns for the origin wave while it is still not ready.
var originWaveProbeInterval = time.Minute

// maybeRefreshOriginWave re-probes ClickHouse for origin columns when either
// table is not ready yet. It runs at most once per originWaveProbeInterval.
// Only the origin atomic flags are updated (false→true); readyFlags is not
// touched after the initial ensureSchema probe.
func (c *CH) maybeRefreshOriginWave() {
	if c == nil {
		return
	}
	if c.originEvents.Load() && c.originConv.Load() {
		return
	}
	if c.conn == nil && c.originColumnReader == nil {
		return
	}
	now := time.Now()
	c.originProbeMu.Lock()
	defer c.originProbeMu.Unlock()
	if c.originEvents.Load() && c.originConv.Load() {
		return
	}
	if !c.originLastProbe.IsZero() && now.Sub(c.originLastProbe) < originWaveProbeInterval {
		return
	}
	c.originLastProbe = now
	have, err := c.probeColumnSetForOrigin()
	if err != nil {
		log.Printf("clickhouse: schema: %v", err)
		return
	}
	flags := readyFromColumns(have)
	if flags["events"][waveOrigin] {
		c.originEvents.Store(true)
	}
	if flags["conversions"][waveOrigin] {
		c.originConv.Store(true)
	}
}

// probeColumnSetForOrigin reads system.columns or the test hook.
func (c *CH) probeColumnSetForOrigin() (map[string]map[string]bool, error) {
	if c.originColumnReader != nil {
		return c.originColumnReader()
	}
	return c.readColumnSet()
}
