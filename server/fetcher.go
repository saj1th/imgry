package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/goware/go-metrics"
	"github.com/goware/lg"
	"github.com/goware/urlx"
	"golang.org/x/net/context/ctxhttp"
)

var (
	DefaultFetcherThroughput     = 100
	DefaultFetcherReqNumAttempts = 2
	// DefaultFetcherReqTimeout = 60 * time.Second
)

// TODO: get Throughput from app.Config.Limits.MaxFetchers

type Fetcher struct {
	Client    *http.Client
	Transport *http.Transport

	Throughput     int // TODO
	ReqNumAttempts int
	HostKeepAlive  time.Duration

	// TODO: lru cache of responses.. like a reverse cache.. including bad urls.. 404s, etc...
	// hmm.. transport for httpcaching ...
}

// TODO: keep-alives / persistent connections
// I've noticed that Go's http client doesn't clean up the idle connections
// for a large number of hosts. Instead we will have to call hf.Transport.CloseIdleConnections()
// every HostKeepAlive duration (assuming > 1 second)

type FetcherResponse struct {
	URL    *url.URL
	Status int
	Data   []byte
	Err    error
}

func NewFetcher() *Fetcher {
	hf := &Fetcher{}
	hf.ReqNumAttempts = DefaultFetcherReqNumAttempts
	hf.HostKeepAlive = 60 * time.Second
	return hf
}

func (hf Fetcher) client() *http.Client {
	if hf.Client != nil {
		return hf.Client
	}

	hf.Transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			// Timeout:   DefaultFetcherReqTimeout,
			KeepAlive: hf.HostKeepAlive,
		}).Dial,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		TLSHandshakeTimeout: 5 * time.Second,
		MaxIdleConnsPerHost: 2,
		DisableCompression:  true,
		DisableKeepAlives:   true,
		// ResponseHeaderTimeout: DefaultFetcherReqTimeout,
	}

	hf.Client = &http.Client{
		// Timeout:   hf.ReqTimeout,
		Transport: hf.Transport,
	}

	return hf.Client
}

func (f Fetcher) Get(ctx context.Context, url string) (*FetcherResponse, error) {
	resps, err := f.GetAll(ctx, []string{url})
	if err != nil {
		return nil, err
	}
	if len(resps) == 0 {
		return nil, errors.New("fetcher: no response")
	}
	resp := resps[0]
	if resp.Err != nil {
		return resp, resp.Err
	}
	return resp, nil
}

func (f Fetcher) GetAll(ctx context.Context, urls []string) ([]*FetcherResponse, error) {
	defer metrics.MeasureSince([]string{"fn.FetchRemoteData"}, time.Now())

	fetches := make([]*FetcherResponse, len(urls))

	var wg sync.WaitGroup
	wg.Add(len(urls))

	// TODO: add thruput here..

	for i, urlStr := range urls {
		fetches[i] = &FetcherResponse{}

		go func(fetch *FetcherResponse) {
			defer wg.Done()

			url, err := urlx.Parse(urlStr)
			if err != nil {
				fetch.Err = err
				return
			}
			fetch.URL = url

			lg.Infof("Fetching %s", url.String())

			resp, err := ctxhttp.Get(ctx, f.client(), url.String())
			if err != nil {
				lg.Warnf("Error fetching %s because %s", url.String(), err)
				fetch.Err = err
				return
			}
			defer resp.Body.Close()

			fetch.Status = resp.StatusCode

			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				fetch.Err = err
				return
			}
			fetch.Data = body
			fetch.Err = nil

		}(fetches[i])
	}

	wg.Wait()
	return fetches, nil
}
