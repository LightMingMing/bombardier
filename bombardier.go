package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/codesenberg/bombardier/internal"

	"github.com/cheggaaa/pb"
	fhist "github.com/codesenberg/concurrent/float64/histogram"
	uhist "github.com/codesenberg/concurrent/uint64/histogram"
	"github.com/satori/go.uuid"
)

type bombardier struct {
	bytesRead, bytesWritten int64

	// HTTP codes
	req1xx uint64
	req2xx uint64
	req3xx uint64
	req4xx uint64
	req5xx uint64
	others uint64

	errorCount uint64

	conf        config
	barrier     completionBarrier
	ratelimiter limiter
	workers     sync.WaitGroup

	timeTaken time.Duration
	latencies *uhist.Histogram
	requests  *fhist.Histogram

	client   client
	doneChan chan struct{}

	// RPS metrics
	rpl   sync.Mutex
	reqs  int64
	start time.Time

	// Errors
	errors *errorMap

	// Progress bar
	bar *pb.ProgressBar

	// Output
	out      io.Writer
	template *template.Template
}

func newBombardier(c config) (*bombardier, error) {
	if err := c.checkArgs(); err != nil {
		return nil, err
	}
	b := new(bombardier)
	b.conf = c
	b.latencies = uhist.Default()
	b.requests = fhist.Default()

	if b.conf.testType() == counted {
		b.bar = pb.New64(int64(*b.conf.numReqs))
		b.bar.ShowSpeed = true
	} else if b.conf.testType() == timed {
		b.bar = pb.New64(b.conf.duration.Nanoseconds() / 1e9)
		b.bar.ShowCounters = false
		b.bar.ShowPercent = false
	}
	b.bar.ManualUpdate = true

	if b.conf.testType() == counted {
		b.barrier = newCountingCompletionBarrier(*b.conf.numReqs)
	} else {
		b.barrier = newTimedCompletionBarrier(*b.conf.duration)
	}

	if b.conf.rate != nil {
		b.ratelimiter = newBucketLimiter(*b.conf.rate)
	} else {
		b.ratelimiter = &nooplimiter{}
	}

	b.out = os.Stdout

	tlsConfig, err := generateTLSConfig(c)
	if err != nil {
		return nil, err
	}

	var (
		pbody *string
		bsp   bodyStreamProducer
	)
	if c.stream {
		if c.bodyFilePath != "" {
			bsp = func() (io.ReadCloser, error) {
				return os.Open(c.bodyFilePath)
			}
		} else {
			bsp = func() (io.ReadCloser, error) {
				return ioutil.NopCloser(
					proxyReader{strings.NewReader(c.body)},
				), nil
			}
		}
	} else {
		pbody = &c.body
		if c.bodyFilePath != "" {
			var bodyBytes []byte
			bodyBytes, err = ioutil.ReadFile(c.bodyFilePath)
			if err != nil {
				return nil, err
			}
			sbody := string(bodyBytes)
			pbody = &sbody
		}
	}

	var payload *payload
	if c.payloadFile != "" {
		payload, err = loadFromFile(c.payloadFile, strings.Split(c.varNames, ","), c.startLine)
	} else if c.payloadUrl != "" {
		if c.scope == request {
			payload, err = loadFromUrl(c.payloadUrl, strings.Split(c.varNames, ","), c.startLine, uint32(*c.numReqs))
		} else {
			payload, err = loadFromUrl(c.payloadUrl, strings.Split(c.varNames, ","), c.startLine, uint32(c.numConns))
		}
	}

	if err != nil {
		return nil, err
	}

	var (
		resolveUrl     = false
		resolveHerader = false
		resolveBody    = false
	)
	if payload != nil {
		c.url, err = url.PathUnescape(c.url)
		if err != nil {
			return nil, err
		}
		resolveUrl = containsPlaceholder(c.url)

		if c.headers != nil {
			for _, header := range *c.headers {
				if containsPlaceholder(header.value) {
					resolveHerader = true
					break
				}
			}
		}

		if pbody != nil {
			resolveBody = containsPlaceholder(*pbody)
		}
	}

	cc := &clientOpts{
		HTTP2:     false,
		maxConns:  c.numConns,
		timeout:   c.timeout,
		tlsConfig: tlsConfig,

		payload:       payload,
		scope:         c.scope,
		resolveUrl:    resolveUrl,
		resolveHeader: resolveHerader,
		resolveBody:   resolveBody,

		headers:      c.headers,
		url:          c.url,
		method:       c.method,
		body:         pbody,
		bodProd:      bsp,
		bytesRead:    &b.bytesRead,
		bytesWritten: &b.bytesWritten,

		assertions: c.assertions,
	}
	b.client = makeHTTPClient(c.clientType, cc)

	if !b.conf.printProgress {
		b.bar.Output = ioutil.Discard
		b.bar.NotPrint = true
	}

	b.template, err = b.prepareTemplate()
	if err != nil {
		return nil, err
	}

	b.workers.Add(int(c.numConns))
	b.errors = newErrorMap()
	b.doneChan = make(chan struct{}, 2)
	return b, nil
}

func makeHTTPClient(clientType clientTyp, cc *clientOpts) client {
	var cl client
	switch clientType {
	case nhttp1:
		cl = newHTTPClient(cc)
	case nhttp2:
		cc.HTTP2 = true
		cl = newHTTPClient(cc)
	case fhttp:
		fallthrough
	default:
		cl = newFastHTTPClient(cc)
	}
	return cl
}

func (b *bombardier) prepareTemplate() (*template.Template, error) {
	var (
		templateBytes []byte
		err           error
	)
	switch f := b.conf.format.(type) {
	case knownFormat:
		templateBytes = f.template()
	case userDefinedTemplate:
		templateBytes, err = ioutil.ReadFile(string(f))
		if err != nil {
			return nil, err
		}
	default:
		panic("format can't be nil at this point, this is a bug")
	}
	outputTemplate, err := template.New("output-template").
		Funcs(template.FuncMap{
			"WithLatencies": func() bool {
				return b.conf.printLatencies
			},
			"FormatBinary": formatBinary,
			"FormatTimeUs": formatTimeUs,
			"FormatTimeUsUint64": func(us uint64) string {
				return formatTimeUs(float64(us))
			},
			"FloatsToArray": func(ps ...float64) []float64 {
				return ps
			},
			"Multiply": func(num, coeff float64) float64 {
				return num * coeff
			},
			"StringToBytes": func(s string) []byte {
				return []byte(s)
			},
			"UUIDV1": uuid.NewV1,
			"UUIDV2": uuid.NewV2,
			"UUIDV3": uuid.NewV3,
			"UUIDV4": uuid.NewV4,
			"UUIDV5": uuid.NewV5,
		}).Parse(string(templateBytes))

	if err != nil {
		return nil, err
	}
	return outputTemplate, nil
}

func (b *bombardier) writeStatistics(
	code int, msTaken uint64, assertResult assertResult,
) {
	b.latencies.Increment(msTaken)
	b.rpl.Lock()
	b.reqs++
	b.rpl.Unlock()
	var counter *uint64
	switch code / 100 {
	case 1:
		counter = &b.req1xx
	case 2:
		counter = &b.req2xx
	case 3:
		counter = &b.req3xx
	case 4:
		counter = &b.req4xx
	case 5:
		counter = &b.req5xx
	default:
		counter = &b.others
	}
	atomic.AddUint64(counter, 1)

	if !assertResult.successful {
		atomic.AddUint64(&b.errorCount, 1)
	}
}

func (b *bombardier) performSingleRequest(idx uint64) {
	code, msTaken, assertResult, err := b.client.do(idx)
	if err != nil {
		b.errors.add(err)
	}
	b.writeStatistics(code, msTaken, assertResult)
}

func (b *bombardier) worker(idx uint64) {
	done := b.barrier.done()
	for b.barrier.tryGrabWork() {
		if b.ratelimiter.pace(done) == brk {
			break
		}
		b.performSingleRequest(idx)
		b.barrier.jobDone()
	}
}

func (b *bombardier) barUpdater() {
	done := b.barrier.done()
	for {
		select {
		case <-done:
			b.bar.Set64(b.bar.Total)
			b.bar.Update()
			b.bar.Finish()
			if b.conf.printProgress {
				fmt.Fprintln(b.out, "Done!")
			}
			b.doneChan <- struct{}{}
			return
		default:
			current := int64(b.barrier.completed() * float64(b.bar.Total))
			b.bar.Set64(current)
			b.bar.Update()
			time.Sleep(b.bar.RefreshRate)
		}
	}
}

func (b *bombardier) rateMeter() {
	requestsInterval := 10 * time.Millisecond
	if b.conf.rate != nil {
		requestsInterval, _ = estimate(*b.conf.rate, rateLimitInterval)
	}
	requestsInterval += 10 * time.Millisecond
	ticker := time.NewTicker(requestsInterval)
	defer ticker.Stop()
	tick := ticker.C
	done := b.barrier.done()
	for {
		select {
		case <-tick:
			b.recordRps()
			continue
		case <-done:
			b.workers.Wait()
			b.recordRps()
			b.doneChan <- struct{}{}
			return
		}
	}
}

func (b *bombardier) recordRps() {
	b.rpl.Lock()
	duration := time.Since(b.start)
	reqs := b.reqs
	b.reqs = 0
	b.start = time.Now()
	b.rpl.Unlock()

	reqsf := float64(reqs) / duration.Seconds()
	b.requests.Increment(reqsf)
}

func (b *bombardier) bombard() {
	if b.conf.printIntro {
		b.printIntro()
	}
	b.bar.Start()
	bombardmentBegin := time.Now()
	b.start = time.Now()
	for i := uint64(0); i < b.conf.numConns; i++ {
		i := i
		go func() {
			defer b.workers.Done()
			b.worker(i)
		}()
	}
	go b.rateMeter()
	go b.barUpdater()
	b.workers.Wait()
	b.timeTaken = time.Since(bombardmentBegin)
	<-b.doneChan
	<-b.doneChan
}

func (b *bombardier) printIntro() {
	if b.conf.testType() == counted {
		fmt.Fprintf(b.out,
			"Bombarding %v with %v request(s) using %v connection(s)\n",
			b.conf.url, *b.conf.numReqs, b.conf.numConns)
	} else if b.conf.testType() == timed {
		fmt.Fprintf(b.out, "Bombarding %v for %v using %v connection(s)\n",
			b.conf.url, *b.conf.duration, b.conf.numConns)
	}
}

func (b *bombardier) gatherInfo() internal.TestInfo {
	info := internal.TestInfo{
		Spec: internal.Spec{
			NumberOfConnections: b.conf.numConns,

			Method: b.conf.method,
			URL:    b.conf.url,

			Body:         b.conf.body,
			BodyFilePath: b.conf.bodyFilePath,

			CertPath: b.conf.certPath,
			KeyPath:  b.conf.keyPath,

			Stream:     b.conf.stream,
			Timeout:    b.conf.timeout,
			ClientType: internal.ClientType(b.conf.clientType),

			Rate: b.conf.rate,
		},
		Result: internal.Results{
			BytesRead:    b.bytesRead,
			BytesWritten: b.bytesWritten,
			TimeTaken:    b.timeTaken,

			Req1XX: b.req1xx,
			Req2XX: b.req2xx,
			Req3XX: b.req3xx,
			Req4XX: b.req4xx,
			Req5XX: b.req5xx,
			Others: b.others,

			Latencies: b.latencies,
			Requests:  b.requests,
		},
	}

	testType := b.conf.testType()
	info.Spec.TestType = internal.TestType(testType)
	if testType == timed {
		info.Spec.TestDuration = *b.conf.duration
	} else if testType == counted {
		info.Spec.NumberOfRequests = *b.conf.numReqs
	}

	if b.conf.headers != nil {
		for _, h := range *b.conf.headers {
			info.Spec.Headers = append(info.Spec.Headers,
				internal.Header{
					Key:   h.key,
					Value: h.value,
				})
		}
	}

	for _, ewc := range b.errors.byFrequency() {
		info.Result.Errors = append(info.Result.Errors,
			internal.ErrorWithCount{
				Error: ewc.error,
				Count: ewc.count,
			})
	}

	return info
}

func (b *bombardier) printStats() {
	info := b.gatherInfo()
	err := b.template.Execute(b.out, info)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (b *bombardier) redirectOutputTo(out io.Writer) {
	b.bar.Output = out
	b.out = out
}

func (b *bombardier) disableOutput() {
	b.redirectOutputTo(ioutil.Discard)
	b.bar.NotPrint = true
}

//func main() {
//	cfg, err := parser.parse(os.Args)
//	if err != nil {
//		fmt.Println(err)
//		os.Exit(exitFailure)
//	}
//	bombardier, err := newBombardier(cfg)
//	if err != nil {
//		fmt.Println(err)
//		os.Exit(exitFailure)
//	}
//	c := make(chan os.Signal, 1)
//	signal.Notify(c, os.Interrupt)
//	go func() {
//		<-c
//		bombardier.barrier.cancel()
//	}()
//	bombardier.bombard()
//	if bombardier.conf.printResult {
//		bombardier.printStats()
//	}
//}
