package main

import (
	"encoding/json"
	"fmt"
	routing "github.com/qiangxue/fasthttp-routing"
	"github.com/valyala/fasthttp"
	"net"
	"net/http"
	"os"
	"os/signal"
)

type TestingConfig struct {
	NumConns      uint64
	NumReqs       uint64
	Url           string
	Method        string
	Headers       []string
	Body          string
	PayloadFile   string `json:"payloadFile"`
	PayloadUrl    string `json:"payloadUrl"`
	VariableNames string
	StartLine     uint32
	Scope         string
}

type RestStatus struct {
	Code    int    `json:"code"`
	Status  string `json:"status"`
	Message string `json:"error"`
}

type Latency struct {
	Avg         string            `json:"avg"`
	StdDev      string            `json:"stdDev"`
	Max         string            `json:"max"`
	Percentiles map[string]uint64 `json:"percentiles"`
}

type Status struct {
	Req1xx uint64 `json:"req1xx"`
	Req2xx uint64 `json:"req2xx"`
	Req3xx uint64 `json:"req3xx"`
	Req4xx uint64 `json:"req4xx"`
	Req5xx uint64 `json:"req5xx"`
	Others uint64 `json:"other"`
}

type Result struct {
	Url      string  `json:"url"`
	NumConns uint64  `json:"numConns"`
	NumReqs  uint64  `json:"numReqs"`
	Status   Status  `json:"status"`
	Latency  Latency `json:"latency"`
	Tps      string  `json:"tps"`
}

func ErrorHandling(ctx *routing.Context, code int, err error) error {
	status := RestStatus{}
	status.Code = code
	status.Status = http.StatusText(code)
	status.Message = err.Error()
	body, err := json.Marshal(status)
	if err != nil {
		return err
	}
	ctx.SetStatusCode(code)
	ctx.SetContentType("application/json")
	ctx.SetBody(body)
	return nil
}

func GetConfig(ctx *routing.Context) (*config, error) {
	testingConfig := &TestingConfig{}
	if err := json.Unmarshal(ctx.PostBody(), testingConfig); err != nil {
		return nil, err
	}
	config := &config{
		numReqs:     &testingConfig.NumReqs,
		numConns:    testingConfig.NumConns,
		url:         testingConfig.Url,
		method:      testingConfig.Method,
		headers:     &headersList{},
		body:        testingConfig.Body,
		format:      formatFromString("pt"),
		payloadFile: testingConfig.PayloadFile,
		payloadUrl:  testingConfig.PayloadUrl,
		varNames:    testingConfig.VariableNames,
		startLine:   testingConfig.StartLine,
		scope:       getScope(testingConfig.Scope),
	}
	if testingConfig.Headers != nil {
		for _, header := range testingConfig.Headers {
			if err := config.headers.Set(header); err != nil {
				return nil, err
			}
		}
	}
	return config, nil
}

func RequestHandling(ctx *routing.Context) error {
	config, err := GetConfig(ctx)
	if err != nil {
		return ErrorHandling(ctx, http.StatusBadRequest, err)
	}

	bombardier, err := newBombardier(*config)
	if err != nil {
		return ErrorHandling(ctx, http.StatusBadRequest, err)
	}

	bombardier.bombard()
	info := bombardier.gatherInfo()
	percentiles := []float64{0.5, 0.75, 0.9, 0.95, 0.99}
	stats := info.Result.LatenciesStats(percentiles)
	latency := Latency{
		Avg:    fmt.Sprintf("%.2f", stats.Mean/1000),
		Max:    fmt.Sprintf("%.2f", stats.Max/1000),
		StdDev: fmt.Sprintf("%.2f", stats.Stddev/1000),
		Percentiles: map[string]uint64{
			"0.5":  stats.Percentiles[0.5] / 1000,
			"0.75": stats.Percentiles[0.75] / 1000,
			"0.9":  stats.Percentiles[0.9] / 1000,
			"0.95": stats.Percentiles[0.95] / 1000,
			"0.99": stats.Percentiles[0.99] / 1000,
		},
	}

	reqStats := info.Result.RequestsStats(percentiles)
	status := Status{Req1xx: info.Result.Req1XX,
		Req2xx: info.Result.Req2XX,
		Req3xx: info.Result.Req3XX,
		Req4xx: info.Result.Req4XX,
		Req5xx: info.Result.Req5XX,
		Others: info.Result.Others}
	result := Result{
		Url:      config.url,
		NumConns: config.numConns,
		NumReqs:  *config.numReqs,
		Status:   status,
		Latency:  latency,
		Tps:      fmt.Sprintf("%.2f", reqStats.Mean),
	}
	body, err := json.Marshal(result)
	if err != nil {
		return ErrorHandling(ctx, http.StatusInternalServerError, err)
	}
	ctx.SetBody(body)
	ctx.SetStatusCode(http.StatusAccepted)
	ctx.SetContentType("application/json")
	return nil
}

func main() {
	ln, err := net.Listen("tcp4", ":8081")
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(-5)
	}

	router := routing.New()
	api := router.Group("/api")
	api.Post("/pt", RequestHandling)
	router.NotFound(routing.NotFoundHandler)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		_ = ln.Close()
	}()
	_ = fasthttp.Serve(ln, router.HandleRequest)
}
