// Command loadgen simulates HTTP clicks against test campaigns.
// It is a standalone utility and is not built into the service image.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// rfc3339Millis is the UTC timestamp layout. Milliseconds stay in the text so a
// short run can be bounded in SQL. The result is still RFC3339.
const rfc3339Millis = "2006-01-02T15:04:05.000Z07:00"

// maxErrorTexts is how many failure strings a report keeps.
const maxErrorTexts = 5

// userAgents are the browser profiles. An empty entry is sent as an empty
// User-Agent so net/http does not substitute Go-http-client/1.1.
var userAgents = []string{
	"Mozilla/5.0 (Linux; Android 14; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.6778.200 Mobile Safari/537.36",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (iPad; CPU OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Linux; Android 13; SM-X200) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.6778.200 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.6778.200 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15",
	"Mozilla/5.0 (X11; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (Linux; Android 14; sdk_gphone64_arm64 Build/AP2A.240805.005) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.6778.200 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 14; Pixel 9; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/131.0.6778.200 Mobile Safari/537.36",
	"",
}

// languages are Accept-Language values. An empty entry means the header is omitted.
var languages = []string{"ru-RU,ru;q=0.9", "en-US,en", "uk-UA,uk", ""}

// models are the sub_id_3 device names sent on every click.
var models = []string{
	"V2439", "V2506", "sdk_gphone64_arm64", "Pixel 10", "Pixel 9a",
	"SM-A145F", "SM-S928B", "RMX3991", "Secret SC-1224", "iPhone15,3",
}

// keywords are the q values rotated at random across clicks.
var keywords = []string{"loan", "credit", "casino", "bet", "crypto"}

// Options is the load run configuration.
type Options struct {
	URL         string
	Campaigns   []string
	N           int
	Duration    time.Duration
	Concurrency int
	Timeout     time.Duration
}

// Report is the outcome of one load run.
type Report struct {
	Sent       int            `json:"sent"`
	Status     map[int]int    `json:"status"`
	Campaigns  map[string]int `json:"campaigns"`
	Errors     int            `json:"errors"`
	ErrorTexts []string       `json:"error_texts,omitempty"`
	P50        time.Duration  `json:"p50"`
	P95        time.Duration  `json:"p95"`
	P99        time.Duration  `json:"p99"`
	Max        time.Duration  `json:"max"`
	Elapsed    time.Duration  `json:"elapsed"`
	RPS        float64        `json:"rps"`
	Started    string         `json:"started"`
	Finished   string         `json:"finished"`
}

// job is one scheduled click.
type job struct {
	campaign string
}

// result is the outcome of one click attempt.
type result struct {
	campaign string
	status   int
	latency  time.Duration
	err      error
}

// main parses flags, runs the click load until completion or Ctrl+C, and prints the report.
// Interrupted runs still print whatever completed. It does not return.
// It exits with status 1 when flags are invalid or Report.Errors is greater than zero.
func main() {
	opt, format, err := parseFlags(os.Args[1:])
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rep, err := run(ctx, opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := emit(format, rep); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if rep.Errors > 0 {
		os.Exit(1)
	}
}

// emit writes rep to stdout as text or indented JSON.
// format is "text" or "json". It returns the encode or write error, if any.
func emit(format string, rep Report) error {
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	_, err := io.WriteString(os.Stdout, rep.Text())
	return err
}

// parseFlags reads argv into Options and the report format.
// args are the program arguments after the process name.
// It returns an error when parsing fails, report is not text or json, or validate fails.
func parseFlags(args []string) (Options, string, error) {
	fs := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var opt Options
	var campaigns, format string
	fs.StringVar(&opt.URL, "url", "http://engine:8080", "engine base URL")
	fs.StringVar(&campaigns, "campaigns", "", "comma-separated campaign slugs (required)")
	fs.IntVar(&opt.N, "n", 5000, "exact number of requests")
	fs.DurationVar(&opt.Duration, "duration", 60*time.Second, "pace window; <=0 sends with no delay")
	fs.IntVar(&opt.Concurrency, "concurrency", 32, "worker pool size")
	fs.DurationVar(&opt.Timeout, "timeout", 5*time.Second, "per-request timeout")
	fs.StringVar(&format, "report", "text", "report format: text or json")
	if err := fs.Parse(args); err != nil {
		return Options{}, "", err
	}
	opt.URL = strings.TrimRight(strings.TrimSpace(opt.URL), "/")
	opt.Campaigns = splitCampaigns(campaigns)
	if format != "text" && format != "json" {
		return Options{}, "", fmt.Errorf("invalid report %q", format)
	}
	if err := validate(opt); err != nil {
		return Options{}, "", err
	}
	return opt, format, nil
}

// splitCampaigns splits a comma-separated campaign list.
// raw may contain spaces around names; empty pieces are dropped.
// It returns the cleaned names and does not fail.
func splitCampaigns(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// validate checks a run configuration.
// opt is the candidate configuration.
// It returns an error when campaigns is empty, n <= 0, or concurrency <= 0.
func validate(opt Options) error {
	if len(opt.Campaigns) == 0 {
		return errors.New("campaigns is required")
	}
	if opt.N <= 0 {
		return errors.New("n must be positive")
	}
	if opt.Concurrency <= 0 {
		return errors.New("concurrency must be positive")
	}
	return nil
}

// run sends exactly opt.N clicks, paced across opt.Duration, and collects a report.
// ctx stops unsent jobs and aborts in-flight requests; those cancels are omitted from the report.
// It returns an error only when opt is invalid. Network failures stay in Report.Errors.
func run(ctx context.Context, opt Options) (Report, error) {
	if err := validate(opt); err != nil {
		return Report{}, err
	}
	started := time.Now()
	client := newClient(opt.Timeout, opt.Concurrency)
	jobs := make(chan job, opt.Concurrency)
	results := make(chan result, opt.N)
	var wg sync.WaitGroup
	wg.Add(opt.Concurrency)
	for i := 0; i < opt.Concurrency; i++ {
		go func() {
			defer wg.Done()
			worker(ctx, client, opt.URL, jobs, results)
		}()
	}
	go func() {
		schedule(ctx, opt, jobs)
		close(jobs)
		wg.Wait()
		close(results)
	}()
	rep := collect(results, started)
	client.CloseIdleConnections()
	return rep, nil
}

// newClient builds an HTTP client that reuses connections and does not follow redirects.
// timeout is the per-request limit. concurrency is the idle-connection pool size for one host.
// Cookies are not stored. CheckRedirect returns http.ErrUseLastResponse. It does not fail.
func newClient(timeout time.Duration, concurrency int) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			MaxIdleConns:        concurrency,
			MaxIdleConnsPerHost: concurrency,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// schedule enqueues opt.N jobs, choosing campaigns round-robin.
// A positive Duration spaces request i at start+i*(Duration/N). A non-positive Duration does not wait.
// ctx drops the jobs that have not been sent yet. It does not return a value.
func schedule(ctx context.Context, opt Options, jobs chan<- job) {
	var interval time.Duration
	if opt.Duration > 0 {
		interval = opt.Duration / time.Duration(opt.N)
	}
	start := time.Now()
	for i := 0; i < opt.N; i++ {
		if interval > 0 && !waitUntil(ctx, start.Add(interval*time.Duration(i))) {
			return
		}
		if interval <= 0 && ctx.Err() != nil {
			return
		}
		next := job{campaign: opt.Campaigns[i%len(opt.Campaigns)]}
		select {
		case <-ctx.Done():
			return
		case jobs <- next:
		}
	}
}

// waitUntil blocks until at or until ctx is cancelled.
// at is an absolute deadline. It returns false when ctx is done first, and true when the deadline is reached.
func waitUntil(ctx context.Context, at time.Time) bool {
	delay := time.Until(at)
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// worker reads jobs and sends one result per request it actually starts.
// Jobs left in the channel after ctx is cancelled are discarded. It does not return a value.
func worker(ctx context.Context, client *http.Client, base string, jobs <-chan job, out chan<- result) {
	for j := range jobs {
		if ctx.Err() != nil {
			continue
		}
		out <- doRequest(ctx, client, base, j.campaign)
	}
}

// doRequest performs one GET and drains the body so the connection can be reused.
// base is the engine origin and campaign is the slug. The query carries q and sub_id_3.
// It returns the HTTP status, or an error for a bad URL, a transport failure, or a canceled context.
func doRequest(ctx context.Context, client *http.Client, base, campaign string) result {
	started := time.Now()
	raw, err := requestURL(base, campaign)
	if err != nil {
		return result{campaign: campaign, latency: time.Since(started), err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return result{campaign: campaign, latency: time.Since(started), err: err}
	}
	applyHeaders(req)
	resp, err := client.Do(req)
	latency := time.Since(started)
	if err != nil {
		return result{campaign: campaign, latency: latency, err: err}
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return result{campaign: campaign, latency: latency, err: err}
	}
	return result{campaign: campaign, status: resp.StatusCode, latency: latency}
}

// requestURL builds GET {base}/{campaign}?q={keyword}&sub_id_3={model}.
// base is the origin without a trailing slash; campaign is one path segment.
// It returns the absolute URL, or an error when base is not a URL.
func requestURL(base, campaign string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + url.PathEscape(campaign)
	q := u.Query()
	q.Set("q", keywords[rand.IntN(len(keywords))])
	q.Set("sub_id_3", models[rand.IntN(len(models))])
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// applyHeaders sets a random User-Agent, Accept-Language, and optional Referer.
// req is the outbound click. An empty user agent is stored as "" so the header is
// not written; an empty language is left unset. Referer is present on half the clicks.
// It does not return a value and does not fail.
func applyHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgents[rand.IntN(len(userAgents))])
	if lang := languages[rand.IntN(len(languages))]; lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	if rand.IntN(2) == 1 {
		req.Header.Set("Referer", "https://example.com/")
	}
}

// collect folds click results into a report.
// started is the run's start instant and keeps its monotonic clock for Elapsed.
// Context cancellations are dropped. It does not fail.
func collect(results <-chan result, started time.Time) Report {
	rep := Report{
		Status:    make(map[int]int),
		Campaigns: make(map[string]int),
	}
	lats := make([]time.Duration, 0, 64)
	for r := range results {
		if r.err != nil && errors.Is(r.err, context.Canceled) {
			continue
		}
		rep.Sent++
		lats = append(lats, r.latency)
		if r.err != nil {
			rep.Errors++
			if len(rep.ErrorTexts) < maxErrorTexts {
				rep.ErrorTexts = append(rep.ErrorTexts, r.err.Error())
			}
			continue
		}
		rep.Status[r.status]++
		rep.Campaigns[r.campaign]++
	}
	finished := time.Now()
	rep.Started = started.UTC().Format(rfc3339Millis)
	rep.Finished = finished.UTC().Format(rfc3339Millis)
	rep.Elapsed = finished.Sub(started)
	if sec := rep.Elapsed.Seconds(); sec > 0 {
		rep.RPS = float64(rep.Sent) / sec
	}
	rep.P50, rep.P95, rep.P99, rep.Max = percentiles(lats)
	return rep
}

// percentiles returns the nearest-rank p50, p95, p99, and the maximum sample.
// samples may be unsorted and is not modified. An empty slice yields zeros.
// Rank is ceil(p*n/100). It does not fail.
func percentiles(samples []time.Duration) (time.Duration, time.Duration, time.Duration, time.Duration) {
	if len(samples) == 0 {
		return 0, 0, 0, 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return percentile(sorted, 50), percentile(sorted, 95), percentile(sorted, 99), sorted[len(sorted)-1]
}

// percentile returns the nearest-rank value at percent from an ascending slice.
// percent is 1..100. It returns 0 when sorted is empty, and does not fail.
func percentile(sorted []time.Duration, percent int) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := (percent*n + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// Text renders r as a plain-text summary.
// The result includes sent, p95, and one line per status code. It does not fail.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "sent %d errors %d\n", r.Sent, r.Errors)
	fmt.Fprintf(&b, "elapsed %s rps %.2f\n", r.Elapsed, r.RPS)
	fmt.Fprintf(&b, "p50 %s p95 %s p99 %s max %s\n", r.P50, r.P95, r.P99, r.Max)
	fmt.Fprintf(&b, "started %s finished %s\n", r.Started, r.Finished)
	codes := make([]int, 0, len(r.Status))
	for code := range r.Status {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	for _, code := range codes {
		fmt.Fprintf(&b, "status %d %d\n", code, r.Status[code])
	}
	names := make([]string, 0, len(r.Campaigns))
	for name := range r.Campaigns {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "campaign %s %d\n", name, r.Campaigns[name])
	}
	for _, msg := range r.ErrorTexts {
		fmt.Fprintf(&b, "error %s\n", msg)
	}
	return b.String()
}
