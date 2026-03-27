package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"sort"
	"time"
)

type result struct {
	ok       bool
	code     int
	dur      time.Duration
	errClass string
}

func main() {
	base := flag.String("base", "http://127.0.0.1:18080", "mock server base url")
	nOK := flag.Int("ok", 50, "number of OK targets")
	nSlow := flag.Int("slow", 200, "number of slow targets")
	slowMs := flag.Int("slowms", 100, "slow latency in ms")
	nUnreach := flag.Int("unreach", 50, "number of unreachable targets")
	unreachURL := flag.String("unreachurl", "http://127.0.0.1:59999/ok", "unreachable url (no listener)")
	rounds := flag.Int("rounds", 30, "rounds")
	timeoutMs := flag.Int("timeout", 2000, "per-request timeout in ms")
	flag.Parse()

	urls := make([]string, 0, *nOK+*nSlow+*nUnreach)
	for i := 0; i < *nOK; i++ {
		urls = append(urls, *base+"/ok")
	}
	for i := 0; i < *nSlow; i++ {
		urls = append(urls, fmt.Sprintf("%s/slow?ms=%d", *base, *slowMs))
	}
	for i := 0; i < *nUnreach; i++ {
		urls = append(urls, *unreachURL)
	}

	client := newHTTPClient(time.Duration(*timeoutMs) * time.Millisecond)

	// CSV header
	fmt.Println("round,total,ok,fail,round_ms,p50_ms,p95_ms,goroutines,alloc_mb,sys_mb")

	for r := 1; r <= *rounds; r++ {
		start := time.Now()
		results := probeRound(client, urls, time.Duration(*timeoutMs)*time.Millisecond)
		roundDur := time.Since(start)

		okCnt, failCnt := 0, 0
		durs := make([]time.Duration, 0, len(results))
		for _, x := range results {
			if x.ok {
				okCnt++
				durs = append(durs, x.dur)
			} else {
				failCnt++
			}
		}
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		p50 := quantile(durs, 0.50)
		p95 := quantile(durs, 0.95)

		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)

		fmt.Printf("%d,%d,%d,%d,%d,%d,%d,%d,%.2f,%.2f\n",
			r, len(urls), okCnt, failCnt,
			roundDur.Milliseconds(),
			p50.Milliseconds(),
			p95.Milliseconds(),
			runtime.NumGoroutine(),
			float64(ms.Alloc)/1024.0/1024.0,
			float64(ms.Sys)/1024.0/1024.0,
		)

		time.Sleep(200 * time.Millisecond)
	}
}

func probeRound(client *http.Client, urls []string, timeout time.Duration) []result {
	ch := make(chan result, len(urls))
	for _, u := range urls {
		u := u
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			ch <- probeOne(ctx, client, u)
		}()
	}
	out := make([]result, 0, len(urls))
	for i := 0; i < len(urls); i++ {
		out = append(out, <-ch)
	}
	return out
}

func probeOne(ctx context.Context, client *http.Client, url string) result {
	start := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := client.Do(req)
	d := time.Since(start)

	if err != nil {
		return result{ok: false, dur: d, errClass: classifyErr(err)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	// 这里“ok”的定义：2xx 视为成功（更符合可用性探测口径）
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	return result{ok: ok, code: resp.StatusCode, dur: d}
}

func classifyErr(err error) string {
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return "timeout"
	}
	var opErr *net.OpError
	if ok := errorAs(err, &opErr); ok {
		return "netop"
	}
	return "other"
}

// 兼容 Go1.20+ 的 errors.As，避免额外引入
func errorAs(err error, target interface{}) bool {
	type aser interface{ As(any) bool }
	if e, ok := any(err).(aser); ok {
		return e.As(target)
	}
	return false
}

func quantile(d []time.Duration, q float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	if q <= 0 {
		return d[0]
	}
	if q >= 1 {
		return d[len(d)-1]
	}
	idx := int(float64(len(d)-1) * q)
	return d[idx]
}

func newHTTPClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2: true,
		MaxIdleConns:      2000,
		MaxConnsPerHost:   0,
		IdleConnTimeout:   90 * time.Second,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // 仅用于本地测试
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
}
