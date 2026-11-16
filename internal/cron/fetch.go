package cron

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// httpStatus is a response other than 200. Callers tell a few of them apart:
// 304 (nothing new), 404 (the API does not know the thing), 401/403/429 (no
// point in asking again during this run).
type httpStatus struct {
	host string
	code int
}

func (e *httpStatus) Error() string { return fmt.Sprintf("%s: HTTP %d", e.host, e.code) }

// statusOf returns the HTTP status an error stands for, or 0.
func statusOf(err error) int {
	var s *httpStatus
	if errors.As(err, &s) {
		return s.code
	}
	return 0
}

// get performs a GET and returns the response body of a 200, capped at limit
// bytes. The caller closes it.
func (r *Runner) get(ctx context.Context, rawURL string, hdr map[string]string, user, pass string, limit int64) (io.ReadCloser, error) {
	body, _, err := r.fetch(ctx, rawURL, hdr, user, pass, limit)
	return body, err
}

// fetch is get that also returns the response headers.
func (r *Runner) fetch(ctx context.Context, rawURL string, hdr map[string]string, user, pass string, limit int64) (io.ReadCloser, http.Header, error) {
	return r.send(ctx, fetchRequest{URL: rawURL, Header: hdr, User: user, Pass: pass, Limit: limit})
}

// maxRedirects is how many redirects send follows, as net/http does by default.
const maxRedirects = 10

// fetchRequest describes one download.
type fetchRequest struct {
	URL        string            // used without the spaces around it, see cleanURL
	Header     map[string]string // extra request headers, such as If-Modified-Since
	User, Pass string            // basic auth; sent only when either is not empty
	Limit      int64             // most bytes of body the caller may read
	// Allow, when not nil, must accept URL and every redirect target. A source
	// that may only talk to some hosts sets it.
	Allow func(*url.URL) bool
}

// restricted returns a copy of the runner's client that follows redirects only
// to addresses allow accepts, and at most maxRedirects of them.
func (r *Runner) restricted(allow func(*url.URL) bool) *http.Client {
	c := *r.http
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("too many redirects")
		}
		if !allow(req.URL) {
			return fmt.Errorf("redirect to %s is not allowed", hostOf(req.URL.String()))
		}
		return nil
	}
	return &c
}

// send performs the GET that q describes and returns the body of a 200,
// capped at q.Limit bytes, with the response headers. The caller closes the body.
// A refused address or redirect ends the request with an error that names the
// host only. A non-200 answer is an *httpStatus. Transport errors name the host
// and drop the URL, which may carry a key in its query string.
func (r *Runner) send(ctx context.Context, q fetchRequest) (io.ReadCloser, http.Header, error) {
	rawURL := cleanURL(q.URL)
	client := r.http
	if q.Allow != nil {
		if u, err := url.Parse(rawURL); err != nil || !q.Allow(u) {
			return nil, nil, fmt.Errorf("%s: this source may not use that address", hostOf(rawURL))
		}
		client = r.restricted(q.Allow)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("bad URL %q", rawURL)
	}
	req.Header.Set("User-Agent", "emtds-cron")
	for k, v := range q.Header {
		req.Header.Set(k, v)
	}
	if q.User != "" || q.Pass != "" {
		req.SetBasicAuth(q.User, q.Pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, nil, fmt.Errorf("%s: %w", hostOf(rawURL), err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, nil, &httpStatus{host: hostOf(rawURL), code: resp.StatusCode}
	}
	return &capped{r: io.LimitReader(resp.Body, q.Limit+1), c: resp.Body, left: q.Limit, host: hostOf(rawURL)}, resp.Header, nil
}
func hostOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "source"
}

// capped fails the read instead of silently truncating: half an IP list or
// half a database is worse than none.
type capped struct {
	r    io.Reader
	c    io.Closer
	left int64
	host string
}

func (c *capped) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, fmt.Errorf("%s: response is larger than the allowed size", c.host)
	}
	return n, err
}

func (c *capped) Close() error { return c.c.Close() }
