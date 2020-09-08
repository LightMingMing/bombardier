package main

import (
	"crypto/tls"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
	"golang.org/x/net/http2"
)

type client interface {
	do(idx uint64) (code int, msTaken uint64, err error)
}

type bodyStreamProducer func() (io.ReadCloser, error)

type clientOpts struct {
	HTTP2 bool

	maxConns  uint64
	timeout   time.Duration
	tlsConfig *tls.Config

	payload       *payload
	scope         scope
	resolveHeader bool
	resolveUrl    bool
	resolveBody   bool

	headers     *headersList
	url, method string

	body    *string
	bodProd bodyStreamProducer

	bytesRead, bytesWritten *int64
}

type fasthttpClient struct {
	client *fasthttp.HostClient

	payload       *payload
	scope         scope
	resolveUrl    bool
	resolveHeader bool
	resolveBody   bool

	rawUrl    string
	rawHeader *headersList

	headers *fasthttp.RequestHeader
	url     *url.URL
	method  string

	body    *string
	bodProd bodyStreamProducer
}

func newFastHTTPClient(opts *clientOpts) client {
	c := new(fasthttpClient)

	c.payload = opts.payload
	c.resolveUrl = opts.resolveUrl
	c.resolveHeader = opts.resolveHeader
	c.resolveBody = opts.resolveBody

	if c.resolveUrl {
		c.rawUrl = opts.url
	} else {
		u, err := url.Parse(opts.url)
		if err != nil {
			// opts.url guaranteed to be valid at this point
			panic(err)
		}
		c.url = u
	}

	c.client = &fasthttp.HostClient{
		MaxConns:                      int(opts.maxConns),
		ReadTimeout:                   opts.timeout,
		WriteTimeout:                  opts.timeout,
		DisableHeaderNamesNormalizing: true,
		TLSConfig:                     opts.tlsConfig,
		Dial: fasthttpDialFunc(
			opts.bytesRead, opts.bytesWritten,
		),
	}

	if c.resolveHeader {
		c.rawHeader = opts.headers
	} else {
		c.headers = headersToFastHTTPHeaders(opts.headers, nil)
	}

	c.method, c.body = opts.method, opts.body
	c.bodProd = opts.bodProd
	c.payload = opts.payload
	c.scope = opts.scope
	return client(c)
}

func (c *fasthttpClient) do(idx uint64) (
	code int, msTaken uint64, err error,
) {
	// prepare the request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()

	var ctx map[string]string
	if c.payload != nil {
		ctx = c.payload.get(c.scope, idx)
	}

	if c.resolveHeader {
		c.headers = headersToFastHTTPHeaders(c.rawHeader, ctx)
	}
	if c.headers != nil {
		c.headers.CopyTo(&req.Header)
	}
	req.Header.SetMethod(c.method)

	if c.resolveUrl {
		u, err := url.Parse(replace(c.rawUrl, ctx))
		if err != nil {
			return 0, 0, err
		}
		c.url = u
	}
	req.SetRequestURI(c.url.RequestURI())
	c.client.Addr = c.url.Host
	c.client.IsTLS = c.url.Scheme == "https"

	if len(req.Header.Host()) == 0 {
		req.Header.SetHost(c.url.Host)
	}

	if c.body != nil {
		if c.resolveBody {
			req.SetBodyString(replace(*c.body, ctx))
		} else {
			req.SetBodyString(*c.body)
		}
	} else {
		bs, bserr := c.bodProd()
		if bserr != nil {
			return 0, 0, bserr
		}
		req.SetBodyStream(bs, -1)
	}

	// fire the request
	start := time.Now()
	err = c.client.Do(req, resp)
	if err != nil {
		code = -1
	} else {
		code = resp.StatusCode()
	}
	msTaken = uint64(time.Since(start).Nanoseconds() / 1000)

	// release resources
	fasthttp.ReleaseRequest(req)
	fasthttp.ReleaseResponse(resp)

	return
}

type httpClient struct {
	client *http.Client

	payload       *payload
	scope         scope
	resolveUrl    bool
	resolveHeader bool
	resolveBody   bool

	rawUrl    string
	rawHeader *headersList

	headers http.Header
	url     *url.URL
	method  string

	body    *string
	bodProd bodyStreamProducer
}

func newHTTPClient(opts *clientOpts) client {
	c := new(httpClient)
	tr := &http.Transport{
		TLSClientConfig:     opts.tlsConfig,
		MaxIdleConnsPerHost: int(opts.maxConns),
	}
	tr.DialContext = httpDialContextFunc(opts.bytesRead, opts.bytesWritten)
	if opts.HTTP2 {
		_ = http2.ConfigureTransport(tr)
	} else {
		tr.TLSNextProto = make(
			map[string]func(authority string, c *tls.Conn) http.RoundTripper,
		)
	}

	cl := &http.Client{
		Transport: tr,
		Timeout:   opts.timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	c.client = cl
	c.payload = opts.payload
	c.scope = opts.scope
	c.resolveUrl = opts.resolveUrl
	c.resolveHeader = opts.resolveHeader
	c.resolveBody = opts.resolveBody

	if c.resolveHeader {
		c.rawHeader = opts.headers
	} else {
		c.headers = headersToHTTPHeaders(opts.headers, nil)
	}

	c.method, c.body, c.bodProd = opts.method, opts.body, opts.bodProd

	if c.resolveUrl {
		c.rawUrl = opts.url
	} else {
		var err error
		c.url, err = url.Parse(opts.url)
		if err != nil {
			// opts.url guaranteed to be valid at this point
			panic(err)
		}
	}

	return client(c)
}

func (c *httpClient) do(idx uint64) (
	code int, msTaken uint64, err error,
) {
	req := &http.Request{}

	var ctx map[string]string

	if c.payload != nil {
		ctx = c.payload.get(c.scope, idx)
	}

	if c.resolveHeader {
		req.Header = headersToHTTPHeaders(c.rawHeader, ctx)
	} else {
		req.Header = c.headers
	}

	req.Method = c.method
	if c.resolveUrl {
		req.URL, err = url.Parse(replace(c.rawUrl, ctx))
		if err != nil {
			return 0, 0, err
		}
	} else {
		req.URL = c.url
	}

	if host := req.Header.Get("Host"); host != "" {
		req.Host = host
	}

	if c.body != nil {
		var body string
		if c.resolveBody {
			body = replace(*c.body, ctx)
		} else {
			body = *c.body
		}
		br := strings.NewReader(body)
		req.ContentLength = int64(len(body))
		req.Body = ioutil.NopCloser(br)
	} else {
		bs, bserr := c.bodProd()
		if bserr != nil {
			return 0, 0, bserr
		}
		req.Body = bs
	}

	start := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		code = -1
	} else {
		code = resp.StatusCode

		_, berr := io.Copy(ioutil.Discard, resp.Body)
		if berr != nil {
			err = berr
		}

		if cerr := resp.Body.Close(); cerr != nil {
			err = cerr
		}
	}
	msTaken = uint64(time.Since(start).Nanoseconds() / 1000)

	return
}

func headersToFastHTTPHeaders(h *headersList, ctx map[string]string) *fasthttp.RequestHeader {
	if len(*h) == 0 {
		return nil
	}
	res := new(fasthttp.RequestHeader)
	for _, header := range *h {
		res.Set(header.key, replace(header.value, ctx))
	}
	return res
}

func headersToHTTPHeaders(h *headersList, ctx map[string]string) http.Header {
	if len(*h) == 0 {
		return http.Header{}
	}
	headers := http.Header{}

	for _, header := range *h {
		headers[header.key] = []string{replace(header.value, ctx)}
	}
	return headers
}
