//go:build wasip1

// Command net is the playground's networking image: a stock Go wasip1
// program on github.com/stealthrocket/net (the WasmEdge socket calls), as
// a learner's own image would be.
//
//	net serve [PORT]            HTTP server (default 80): GET /NAME answers "hello NAME from ADDR",
//	                            GET /bulk?bytes=N streams N bytes
//	net get URL [N]             N GETs (default 1), bodies on stdout
//	net bench URL [N]           N sequential GETs (latency p50/p90) and one 8 MiB GET (throughput)
//	net udp-echo [PORT]         echoes datagrams (default 7)
//	net udp HOST:PORT MSG       sends MSG, prints the reply
//	net lookup NAME             prints NAME's addresses (the network's DNS)
//	net dial HOST:PORT          dials, prints the result
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stealthrocket/net/wasip1"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: net serve|get|bench|udp-echo|udp|lookup|dial ...")
		os.Exit(2)
	}
	arg := func(i int, def string) string {
		if len(os.Args) > i {
			return os.Args[i]
		}
		return def
	}
	// A server started a moment ago (run -d) may not listen yet: the
	// clients retry a refused connection for a few seconds.
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		deadline := time.Now().Add(10 * time.Second)
		for {
			c, err := wasip1.DialContext(ctx, network, addr)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "refused") || time.Now().After(deadline) {
				return c, err
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: dial, MaxIdleConnsPerHost: 1}}
	switch os.Args[1] {
	case "serve":
		l, err := wasip1.Listen("tcp", ":"+arg(2, "80"))
		check(err)
		fmt.Println("listening on", l.Addr())
		chunk := make([]byte, 64<<10)
		check(http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/bulk" {
				n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
				n = min(max(n, 0), 64<<20)
				w.Header().Set("Content-Length", strconv.Itoa(n))
				for n > 0 {
					k, err := w.Write(chunk[:min(n, len(chunk))])
					if err != nil {
						return
					}
					n -= k
				}
				return
			}
			fmt.Fprintf(w, "hello %s from %s\n", strings.TrimPrefix(r.URL.Path, "/"), r.RemoteAddr)
		})))
	case "get":
		n, _ := strconv.Atoi(arg(3, "1"))
		for i := 0; i < n; i++ {
			resp, err := client.Get(arg(2, "http://localhost/"))
			check(err)
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			check(err)
			fmt.Print(string(b))
		}
	case "bench":
		base := strings.TrimSuffix(arg(2, "http://localhost"), "/")
		n, _ := strconv.Atoi(arg(3, "100"))
		n = min(max(n, 1), 10000)
		var lat []time.Duration
		for i := 0; i < n; i++ {
			t0 := time.Now()
			resp, err := client.Get(base + "/bench")
			check(err)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lat = append(lat, time.Since(t0))
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		const bulk = 8 << 20
		t0 := time.Now()
		resp, err := client.Get(fmt.Sprintf("%s/bulk?bytes=%d", base, bulk))
		check(err)
		got, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		check(err)
		el := time.Since(t0)
		fmt.Printf("%d requests: p50 %.2f ms, p90 %.2f ms; %d bytes in %.1f ms: %.1f MB/s\n", n,
			ms(lat[n/2]), ms(lat[n*9/10]), got, ms(el), float64(got)/el.Seconds()/1e6)
	case "udp-echo":
		pc, err := wasip1.ListenPacket("udp", ":"+arg(2, "7"))
		check(err)
		fmt.Println("echoing on", pc.LocalAddr())
		buf := make([]byte, 8192)
		for {
			k, from, err := pc.ReadFrom(buf)
			check(err)
			pc.WriteTo(buf[:k], from)
		}
	case "udp":
		c, err := wasip1.Dial("udp", arg(2, "localhost:7"))
		check(err)
		// UDP has no connection: resend every half second, 20 times at
		// most, until a reply comes (the echo may not be bound yet).
		buf := make([]byte, 8192)
		for i := 0; ; i++ {
			_, err = c.Write([]byte(arg(3, "ping")))
			check(err)
			c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			k, err := c.Read(buf)
			if err == nil {
				fmt.Printf("reply: %s\n", buf[:k])
				return
			}
			if i == 19 {
				check(err)
			}
		}
	case "lookup":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ips, err := net.DefaultResolver.LookupHost(ctx, arg(2, "localhost"))
		check(err)
		fmt.Println(strings.Join(ips, " "))
	case "dial":
		c, err := wasip1.Dial("tcp", arg(2, "localhost:80"))
		check(err)
		fmt.Println("connected to", c.RemoteAddr())
		c.Close()
	default:
		fmt.Fprintln(os.Stderr, "net: unknown command", os.Args[1])
		os.Exit(2)
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "net:", err)
		os.Exit(1)
	}
}
