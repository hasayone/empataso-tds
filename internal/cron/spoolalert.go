package cron

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

// Spool alert. The engines park click events on disk while ClickHouse is away;
// that is the system working, and it needs no alarm for minutes. It needs one
// when the queue stays for long with ClickHouse connected, when segments are set
// aside, and when the safety nets are off. The check reads the same metrics
// files as the loss check and uses the same raise, remind and clear rule.

const (
	// spoolBacklogAge is how old the oldest parked segment may be while ClickHouse
	// is connected before the queue counts as stuck.
	spoolBacklogAge = 10 * time.Minute

	// The reasons of a spool alert. They are the words of the admin notice line.
	spoolReasonBacklog  = "backlog"  // segments wait for longer than spoolBacklogAge with ClickHouse connected
	spoolReasonRejected = "rejected" // the rejected directory grew
	spoolReasonNoDedup  = "nodedup"  // connected, but the events table does not deduplicate
	spoolReasonNoSpool  = "nospool"  // ClickHouse is configured and the engine has no disk queue
	// Reasons of the postback path.
	spoolReasonPostbackLog  = "pblog"    // journal lines were lost
	spoolReasonOutboundDrop = "outdrop"  // outbound jobs were dropped without a disk copy
	spoolReasonRetryExpired  = "retryexp" // postbacks were written off: their click never appeared
	spoolReasonRetryOverflow = "retryover" // a signed click was refused: the retry ceiling was full
	spoolReasonQueueFull     = "qfull"   // a postback was refused: the store queue was full
	spoolReasonPostbackQ     = "pbqueue" // the postback or outbound queue is stuck or set segments aside
)

// spoolReasons lists every reason in the order the alert names them.
var spoolReasons = []string{
	spoolReasonBacklog, spoolReasonRejected, spoolReasonNoDedup, spoolReasonNoSpool,
	spoolReasonPostbackLog, spoolReasonOutboundDrop, spoolReasonRetryExpired,
	spoolReasonRetryOverflow, spoolReasonQueueFull, spoolReasonPostbackQ,
}

// SpoolBase is what the last check knew about the spool of one engine instance:
// the rejected count to compare the next one with, and since when the engine has
// been seen without deduplication or without a spool. A rejected count that
// fell (someone cleaned the directory) becomes the new base. StartedAt is the
// process the figures come from, Drops its postback loss counters and
// RejectedPostbacks and RejectedOutbound the rejected counts of the postback and
// outbound queues. A base without StartedAt (written before these fields
// existed) only sets the new figures and raises nothing.
type SpoolBase struct {
	Rejected          int64             `json:"rejected"`
	NoDedupSince      time.Time         `json:"no_dedup_since,omitempty"`
	NoSpoolSince      time.Time         `json:"no_spool_since,omitempty"`
	StartedAt         time.Time         `json:"started_at,omitempty"`
	Drops             config.QueueDrops `json:"drops"`
	RejectedPostbacks int64             `json:"rejected_postbacks"`
	RejectedOutbound  int64             `json:"rejected_outbound"`
}

// SpoolState is the last spool check, kept in the status file. Alerted stays
// true from the alert until the engines have been free of every reason for
// RecoverAfter. Reasons and Instances name what the last check that found trouble
// found; they stay readable after the alert clears. Bases holds each instance.
type SpoolState struct {
	CheckedAt  time.Time            `json:"checked_at"`
	Alerted    bool                 `json:"alerted"`
	AlertedAt  time.Time            `json:"alerted_at,omitempty"`
	QuietSince time.Time            `json:"quiet_since,omitempty"`
	Reasons    []string             `json:"reasons,omitempty"`
	Instances  []string             `json:"instances,omitempty"`
	Bases      map[string]SpoolBase `json:"bases,omitempty"`
}

// since returns when a condition that holds now started: prev when it was
// already running, now when it starts, the zero time when it does not hold.
func since(prev, now time.Time, holds bool) time.Time {
	switch {
	case !holds:
		return time.Time{}
	case prev.IsZero():
		return now
	}
	return prev
}

// instanceDrops returns what the postback loss counters of one fresh instance
// grew by since base, the state of the last check. With the same StartedAt it is
// the growth of the counters. After a restart it is everything the new process
// has counted plus the tail of the process before it: m.Previous against base
// when base is that very process, or all of Previous when the check never saw
// it. A Previous older than base was counted before and adds nothing. It does
// not fail.
func instanceDrops(base SpoolBase, m config.EngineMetrics) config.QueueDrops {
	cur := m.Drops()
	if base.StartedAt.Equal(m.StartedAt) {
		return cur.Minus(base.Drops)
	}
	if p := m.Previous; p != nil && !p.StartedAt.Before(base.StartedAt) {
		var seen config.QueueDrops
		if p.StartedAt.Equal(base.StartedAt) {
			seen = base.Drops
		}
		cur = cur.Plus(p.Drops.Minus(seen))
	}
	return cur
}

// postbackReasons returns the reasons of the postback path for one fresh
// instance m. base is the last check's figures; counted is false for an
// instance never seen (or seen before the base held these figures): then only the
// stuck queues count, the growth of the counters and of the rejected directories
// has nothing to be compared with. The reasons are a growth of the journal loss,
// of the dropped outbound jobs or of the written-off postbacks, and a queue of
// postbacks or outbound jobs that is stuck (the oldest segment older than
// spoolBacklogAge; the postback queue counts only while ClickHouse is connected,
// because it waits for the store) or that set segments aside. It does not fail.
func postbackReasons(base SpoolBase, counted bool, m config.EngineMetrics) []string {
	var why []string
	if counted {
		d := instanceDrops(base, m)
		for _, c := range []struct {
			n      int64
			reason string
		}{{d.PostbackLog, spoolReasonPostbackLog}, {d.Outbound, spoolReasonOutboundDrop}, {d.RetryExpired, spoolReasonRetryExpired}, {d.RetryOverflow, spoolReasonRetryOverflow}, {d.QueueFull, spoolReasonQueueFull}} {
			if c.n > 0 {
				why = append(why, c.reason)
			}
		}
	}
	age := int64(spoolBacklogAge / time.Second)
	pb, out := m.Spool.Postbacks, m.Spool.Outbound
	stuck := pb.Segments > 0 && pb.OldestAgeSeconds > age && m.Events.Configured && m.Events.Connected
	stuck = stuck || out.Segments > 0 && out.OldestAgeSeconds > age
	rejected := counted && (pb.Rejected > base.RejectedPostbacks || out.Rejected > base.RejectedOutbound)
	if stuck || rejected {
		why = append(why, spoolReasonPostbackQ)
	}
	return why
}

// decideSpool computes the state after one check over the metrics files and what
// to send. prev is the last state, nil before the first check; now is the time
// of the check; a.Repeat and a.RecoverAfter are the loss alert's.
//
// Per fresh instance, the reasons are those of postbackReasons and: the oldest
// segment older than
// spoolBacklogAge with ClickHouse connected; a rejected count above the base
// (an instance never seen before only sets its base); ClickHouse connected and
// the table not deduplicating, or ClickHouse configured and no disk queue, for
// at least lossFailingAfter by the file's own clock. The alert is hot with any
// reason. It is calm after a.RecoverAfter without a reason or a condition still
// waiting out its minute; a check with no fresh file restarts that wait, as the
// loss check does. It does not fail.
func decideSpool(prev *SpoolState, metrics []config.EngineMetrics, a LossAlert, now time.Time) (SpoolState, alertVerdict) {
	next := SpoolState{CheckedAt: now, Bases: map[string]SpoolBase{}}
	var old map[string]SpoolBase
	flag := alertFlag{}
	if prev != nil {
		old = prev.Bases
		flag = alertFlag{On: prev.Alerted, At: prev.AlertedAt}
		next.QuietSince, next.Reasons, next.Instances = prev.QuietSince, prev.Reasons, prev.Instances
	}
	fresh, waiting := 0, false
	reasons := map[string]bool{}
	var hit []string
	for _, m := range metrics {
		name := m.Instance
		base, known := old[name]
		if now.Sub(m.UpdatedAt) > LossStale {
			if known {
				next.Bases[name] = base
			}
			continue
		}
		fresh++
		ev := m.Events
		cur := SpoolBase{
			Rejected: ev.Spool.Rejected, StartedAt: m.StartedAt, Drops: m.Drops(),
			RejectedPostbacks: m.Spool.Postbacks.Rejected, RejectedOutbound: m.Spool.Outbound.Rejected,
		}
		var why []string
		why = append(why, postbackReasons(base, known && !base.StartedAt.IsZero(), m)...)
		if ev.Configured && ev.Connected && ev.Spool.Segments > 0 && ev.Spool.OldestAgeSeconds > int64(spoolBacklogAge/time.Second) {
			why = append(why, spoolReasonBacklog)
		}
		if known && ev.Spool.Rejected > base.Rejected {
			why = append(why, spoolReasonRejected)
		}
		cur.NoDedupSince = since(base.NoDedupSince, m.UpdatedAt, ev.Configured && ev.Connected && !ev.DedupReady)
		cur.NoSpoolSince = since(base.NoSpoolSince, m.UpdatedAt, ev.Configured && !ev.Spool.Enabled)
		for _, c := range []struct {
			since  time.Time
			reason string
		}{{cur.NoDedupSince, spoolReasonNoDedup}, {cur.NoSpoolSince, spoolReasonNoSpool}} {
			switch {
			case c.since.IsZero():
			case m.UpdatedAt.Sub(c.since) >= lossFailingAfter:
				why = append(why, c.reason)
			default:
				waiting = true
			}
		}
		next.Bases[name] = cur
		if len(why) > 0 {
			hit = append(hit, name)
			for _, r := range why {
				reasons[r] = true
			}
		}
	}
	hot := len(hit) > 0
	if hot {
		sort.Strings(hit)
		next.Instances = hit
		next.Reasons = next.Reasons[:0:0]
		for _, r := range spoolReasons {
			if reasons[r] {
				next.Reasons = append(next.Reasons, r)
			}
		}
	}
	switch {
	case fresh == 0 || hot || waiting:
		next.QuietSince = time.Time{}
	case next.QuietSince.IsZero():
		next.QuietSince = now
	}
	calm := !next.QuietSince.IsZero() && now.Sub(next.QuietSince) >= a.RecoverAfter
	flag, v := decideAlert(flag, hot, calm, a.Repeat, now)
	next.Alerted, next.AlertedAt = flag.On, flag.At
	return next, v
}

// spoolText is one language of the spool alert: the Telegram raise and clear
// messages, and the sentence for each reason.
type spoolText struct {
	raise, clear string
	reason       map[string]string
}

// spoolTexts holds the spool alert messages by language.
var spoolTexts = map[string]*spoolText{
	"en": {
		raise: "⚠️ EmTDS: the disk queues (click events, postbacks) need attention (%s): %s",
		clear: "✅ EmTDS: the disk queues (click events, postbacks) are in order again (%s).",
		reason: map[string]string{
			spoolReasonBacklog:      "events have waited on disk for more than 10 minutes although ClickHouse is connected",
			spoolReasonRejected:     "segments were set aside in rejected/ and are not written; look at them before deleting",
			spoolReasonNoDedup:      "the events table does not deduplicate, so a batch that may be stored is not repeated and is counted as lost",
			spoolReasonNoSpool:      "the engine runs without a disk queue: events are lost when ClickHouse is unavailable; mount the spool volume",
			spoolReasonPostbackLog:  "postback journal lines were lost",
			spoolReasonOutboundDrop:  "outbound postbacks were dropped: the send queue was full and had no disk copy, or the network never answered within the attempt ceiling",
			spoolReasonRetryExpired:  "postbacks were written off: their click never appeared within the wait",
			spoolReasonRetryOverflow: "a signed click was refused with 404 because the retry queue was full, so the lead was not kept",
			spoolReasonQueueFull:     "a postback was refused with 503 because the store queue was full",
			spoolReasonPostbackQ:     "the postback or outbound queue is stuck for more than 10 minutes or set segments aside in rejected/",
		},
	},
	"ru": {
		raise: "⚠️ EmTDS: дисковые очереди (события кликов, постбэки) требуют внимания (%s): %s",
		clear: "✅ EmTDS: дисковые очереди (события кликов, постбэки) снова в порядке (%s).",
		reason: map[string]string{
			spoolReasonBacklog:      "события ждут на диске дольше 10 минут, хотя ClickHouse подключён",
			spoolReasonRejected:     "сегменты отложены в rejected/ и не записываются; посмотрите их, прежде чем удалять",
			spoolReasonNoDedup:      "таблица events не дедуплицирует, поэтому пачка с неизвестной судьбой не повторяется и считается потерянной",
			spoolReasonNoSpool:      "engine работает без дисковой очереди: при недоступном ClickHouse события теряются; подключите том спула",
			spoolReasonPostbackLog:  "строки журнала постбэков потеряны",
			spoolReasonOutboundDrop:  "исходящие постбэки сброшены: очередь отправки была полна и копии на диске не было, либо сеть не ответила за отведённые попытки",
			spoolReasonRetryExpired:  "постбэки списаны: клик не появился за время ожидания",
			spoolReasonRetryOverflow: "подписанный клик отклонён с 404: очередь повторов была полна, лид не сохранён",
			spoolReasonQueueFull:     "постбэк отклонён с 503: очередь на время сбоя хранилища была полна",
			spoolReasonPostbackQ:     "очередь постбэков или исходящих застряла дольше 10 минут либо отложила сегменты в rejected/",
		},
	},
	"uk": {
		raise: "⚠️ EmTDS: дискові черги (події кліків, постбеки) потребують уваги (%s): %s",
		clear: "✅ EmTDS: дискові черги (події кліків, постбеки) знову в порядку (%s).",
		reason: map[string]string{
			spoolReasonBacklog:      "події чекають на диску довше 10 хвилин, хоча ClickHouse підключено",
			spoolReasonRejected:     "сегменти відкладено в rejected/ і не записуються; перегляньте їх, перш ніж видаляти",
			spoolReasonNoDedup:      "таблиця events не дедуплікує, тому пачка з невідомою долею не повторюється і вважається втраченою",
			spoolReasonNoSpool:      "engine працює без дискової черги: коли ClickHouse недоступний, події втрачаються; підключіть том спулу",
			spoolReasonPostbackLog:  "рядки журналу постбеків втрачено",
			spoolReasonOutboundDrop:  "вихідні постбеки скинуто: черга відправки була повна і копії на диску не було, або мережа не відповіла за відведені спроби",
			spoolReasonRetryExpired:  "постбеки списано: клік не з'явився за час очікування",
			spoolReasonRetryOverflow: "підписаний клік відхилено з 404: черга повторів була повна, лід не збережено",
			spoolReasonQueueFull:     "постбек відхилено з 503: черга на час збою сховища була повна",
			spoolReasonPostbackQ:     "черга постбеків або вихідних застрягла довше 10 хвилин або відклала сегменти в rejected/",
		},
	},
}

// spoolMessage returns the Telegram text for v in lang, or "" for alertQuiet.
// st supplies the instances and the reasons. An unknown lang is English.
func spoolMessage(v alertVerdict, st SpoolState, lang string) string {
	t := spoolTexts[lang]
	if t == nil {
		t = spoolTexts["en"]
	}
	who := strings.Join(st.Instances, ", ")
	switch v {
	case alertRaise, alertRemind:
		parts := make([]string, 0, len(st.Reasons))
		for _, r := range st.Reasons {
			parts = append(parts, t.reason[r])
		}
		return fmt.Sprintf(t.raise, who, strings.Join(parts, "; "))
	case alertClear:
		return fmt.Sprintf(t.clear, who)
	}
	return ""
}

// SpoolNotice returns the admin notice line for a raised spool alert. st is the
// cron status and now the current time. ok is false when no alert is raised, no
// reason is known or the last check is older than LossStateFresh. The line is
// "<RFC3339 time> event spool <reasons> (<instances>)", both lists comma-separated;
// the admin panel parses it for its banners.
func SpoolNotice(st Status, now time.Time) (line string, ok bool) {
	s := st.Spool
	if s == nil || !s.Alerted || len(s.Reasons) == 0 || now.Sub(s.CheckedAt) > LossStateFresh {
		return "", false
	}
	return fmt.Sprintf("%s event spool %s (%s)", s.CheckedAt.UTC().Format(time.RFC3339), strings.Join(s.Reasons, ","), strings.Join(s.Instances, ",")), true
}
