package main

import (
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rolandshoemaker/dns"
	"golang.org/x/net/proxy"
)

func randomString() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%X", b)
}

func newDialer() proxy.Dialer {
	randStr := randomString()
	p, err := proxy.SOCKS5(
		"tcp",
		"127.0.0.1:9150",
		&proxy.Auth{User: randStr, Password: randStr},
		&net.Dialer{Timeout: 10 * time.Second},
	)
	if err != nil {
		panic(err)
	}
	return p
}

type basicResult struct {
	LookupTook         time.Duration
	HTTPConnectionTook time.Duration
	Error              string
	Page               string
	IP                 string
}

type result struct {
	Name string

	Tor   *basicResult
	Plain *basicResult
}

type tester struct {
	resolver  *dns.Client
	publicDNS string
	client    *http.Client
	names     chan string
	results   chan result
}

func (t *tester) processName(wg *sync.WaitGroup, name string, client *http.Client, resolver *dns.Client, grabPage bool) (r *basicResult) {
	defer wg.Done()
	r = &basicResult{}
	msg := new(dns.Msg)
	msg.SetEdns0(4096, true)
	msg.SetQuestion(dns.Fqdn(name), dns.TypeA)
	s := time.Now()
	resp, _, err := resolver.Exchange(msg, t.publicDNS)
	r.LookupTook = time.Since(s)
	if err != nil {
		r.Error = err.Error()
		return
	}
	if len(resp.Answer) == 0 {
		r.Error = "No addresses found"
		return
	}
	for _, answer := range resp.Answer {
		if a, ok := answer.(*dns.A); ok {
			r.IP = a.A.String()
			break
		}
	}
	if r.IP == "" {
		r.Error = "Malformed DNS response"
		return
	}
	s = time.Now()
	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/", r.IP), nil)
	if err != nil {
		r.Error = err.Error()
		return
	}
	req.Host = name
	httpResp, err := client.Do(req)
	r.HTTPConnectionTook = time.Since(s)
	if err != nil {
		r.Error = err.Error()
		return
	}
	defer httpResp.Body.Close()
	if grabPage {
		body, err := ioutil.ReadAll(httpResp.Body)
		if err != nil {
			r.Error = err.Error()
		}
		r.Page = string(body)
	}
	return
}

func (t *tester) process(name string) {
	wg := new(sync.WaitGroup)
	wg.Add(2)
	r := result{Name: name}
	go func() { r.Plain = t.processName(wg, name, t.client, t.resolver, false) }()

	proxyDialer := newDialer()
	torResolver := new(dns.Client)
	torResolver.Net = "tcp"
	torResolver.ReadTimeout = 10 * time.Second
	torResolver.Dialer = proxyDialer
	torClient := new(http.Client)
	torClient.Timeout = 10 * time.Second
	torClient.Transport = &http.Transport{
		Dial:                proxyDialer.Dial,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	go func() { r.Tor = t.processName(wg, name, torClient, torResolver, true) }()

	wg.Wait()
	if r.Tor.Error != "" {
		fmt.Println(":(", r.Tor.Error)
	} else {
		fmt.Println(":)")
	}
	t.results <- r
}

func (t *tester) run(workers int) {
	wg := new(sync.WaitGroup)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range t.names {
				t.process(name)
			}
		}()
	}
	wg.Wait()
}

func main() {
	dnsAddr := flag.String("dnsAddr", "8.8.8.8:53", "")
	namesFile := flag.String("namesFile", "names", "")
	resultsFile := flag.String("resultsFile", "results.json", "")
	workers := flag.Int("workers", 1, "")
	flag.Parse()

	t := tester{
		resolver:  new(dns.Client),
		publicDNS: *dnsAddr,
		client:    new(http.Client),
	}
	t.resolver.Net = "tcp"
	t.resolver.ReadTimeout = 10 * time.Second
	t.client.Timeout = 10 * time.Second

	// load names
	names, err := ioutil.ReadFile(*namesFile)
	if err != nil {
		fmt.Println(err)
		return
	}
	splitNames := strings.Split(string(names), "\n")
	t.names = make(chan string, len(splitNames))
	t.results = make(chan result, len(splitNames))
	for _, n := range splitNames {
		t.names <- n
	}
	close(t.names)

	t.run(*workers)

	results := []result{}
	for r := range t.results {
		results = append(results, r)
	}
	jsonResults, err := json.Marshal(results)
	if err != nil {
		fmt.Println(err)
		return
	}
	err = ioutil.WriteFile(*resultsFile, jsonResults, os.ModePerm)
	if err != nil {
		fmt.Println(err)
		return
	}

	// ???
}
