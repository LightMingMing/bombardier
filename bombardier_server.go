package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/buaazp/fasthttprouter"
	"github.com/fasthttp/websocket"
	"github.com/valyala/fasthttp"
)

type Assertion struct {
	Asserter   string
	Expression string
	Condition  string
	Expected   string
}

type Latency struct {
	Avg         string            `json:"avg"`
	StdDev      string            `json:"stdDev"`
	Max         string            `json:"max"`
	Min         string            `json:"min"`
	Percentiles map[string]string `json:"percentiles"`
}

type Status struct {
	Req1xx uint64 `json:"req1xx"`
	Req2xx uint64 `json:"req2xx"`
	Req3xx uint64 `json:"req3xx"`
	Req4xx uint64 `json:"req4xx"`
	Req5xx uint64 `json:"req5xx"`
	Others uint64 `json:"other"`
}

type BombardierRequest struct {
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
	Assertions    []Assertion
}

type BombardierResponse struct {
	Url      string  `json:"url"`
	NumConns uint64  `json:"numConns"`
	NumReqs  uint64  `json:"numReqs"`
	Status   Status  `json:"status"`
	Latency  Latency `json:"latency"`
	Tps      string  `json:"tps"`

	ErrorCount uint64 `json:"errorCount"`
}

type RestStatus struct {
	Code    int    `json:"code"`
	Status  string `json:"status"`
	Message string `json:"error"`
}

func readBombardierRequest(ctx *fasthttp.RequestCtx) (*BombardierRequest, error) {
	request := &BombardierRequest{}
	err := json.Unmarshal(ctx.PostBody(), request)
	return request, err
}

func newConfig(req *BombardierRequest) (*config, error) {

	config := &config{
		numReqs:     &req.NumReqs,
		numConns:    req.NumConns,
		url:         req.Url,
		method:      req.Method,
		headers:     &headersList{},
		body:        req.Body,
		format:      formatFromString("pt"),
		payloadFile: req.PayloadFile,
		payloadUrl:  req.PayloadUrl,
		varNames:    req.VariableNames,
		startLine:   req.StartLine,
		scope:       getScope(req.Scope),
	}
	if req.Headers != nil {
		for _, header := range req.Headers {
			if err := config.headers.Set(header); err != nil {
				return nil, err
			}
		}
	}
	assertions := make([]assertion, 0)
	if req.Assertions != nil {
		for _, a := range req.Assertions {
			assertions = append(assertions, assertion{
				asserter:   a.Asserter,
				expression: a.Expression,
				condition:  a.Condition,
				expected:   a.Expected,
			})
		}
	}
	config.assertions = &assertions
	return config, nil
}

func gatherInfo(bombardier *bombardier) *BombardierResponse {
	info := bombardier.gatherInfo()
	percentiles := []float64{0.25, 0.5, 0.75, 0.9, 0.95, 0.99}
	stats := info.Result.LatenciesStats(percentiles)
	latency := Latency{
		Avg:    fmt.Sprintf("%.2f", stats.Mean/1000),
		Max:    fmt.Sprintf("%.2f", stats.Max/1000),
		Min:    fmt.Sprintf("%.2f", stats.Min/1000),
		StdDev: fmt.Sprintf("%.2f", stats.Stddev/1000),
		Percentiles: map[string]string{
			"0.25": fmt.Sprintf("%.2f", float64(stats.Percentiles[0.25])/1000),
			"0.5":  fmt.Sprintf("%.2f", float64(stats.Percentiles[0.5])/1000),
			"0.75": fmt.Sprintf("%.2f", float64(stats.Percentiles[0.75])/1000),
			"0.9":  fmt.Sprintf("%.2f", float64(stats.Percentiles[0.9])/1000),
			"0.95": fmt.Sprintf("%.2f", float64(stats.Percentiles[0.95])/1000),
			"0.99": fmt.Sprintf("%.2f", float64(stats.Percentiles[0.99])/1000),
		},
	}

	tps := float64(*bombardier.conf.numReqs) / bombardier.timeTaken.Seconds()
	status := Status{Req1xx: info.Result.Req1XX,
		Req2xx: info.Result.Req2XX,
		Req3xx: info.Result.Req3XX,
		Req4xx: info.Result.Req4XX,
		Req5xx: info.Result.Req5XX,
		Others: info.Result.Others}
	return &BombardierResponse{
		Url:        bombardier.conf.url,
		NumConns:   bombardier.conf.numConns,
		NumReqs:    *bombardier.conf.numReqs,
		Status:     status,
		Latency:    latency,
		Tps:        fmt.Sprintf("%.2f", tps),
		ErrorCount: bombardier.errorCount,
	}
}

func errorHandling(ctx *fasthttp.RequestCtx, code int, err error) {
	status := RestStatus{}
	status.Code = code
	status.Status = http.StatusText(code)
	status.Message = err.Error()
	body, err := json.Marshal(status)
	if err == nil {
		ctx.SetContentType("application/json")
		ctx.SetBody(body)
	}
	ctx.SetStatusCode(code)
}

func requestHandling(ctx *fasthttp.RequestCtx) {
	req, err := readBombardierRequest(ctx)
	if err != nil {
		errorHandling(ctx, http.StatusBadRequest, err)
		return
	}

	config, err := newConfig(req)
	if err != nil {
		errorHandling(ctx, http.StatusBadRequest, err)
		return
	}

	bombardier, err := newBombardier(*config)
	if err != nil {
		errorHandling(ctx, http.StatusBadRequest, err)
		return
	}

	bombardier.bombard()

	resp := gatherInfo(bombardier)
	body, err := json.Marshal(resp)
	if err != nil {
		errorHandling(ctx, http.StatusInternalServerError, err)
		return
	}
	ctx.SetBody(body)
	ctx.SetStatusCode(http.StatusAccepted)
	ctx.SetContentType("application/json")
}

var upgrader = websocket.FastHTTPUpgrader{} // use default options

func webSocketRequestHandling(c *websocket.Conn) {
	// defer c.Close()

	_, reqData, err := c.ReadMessage()

	req := &BombardierRequest{}
	if err := json.Unmarshal(reqData, req); err != nil {
		return
	}

	config, err := newConfig(req)
	if err != nil {
		return
	}

	bombardier, err := newBombardier(*config)
	if err != nil {
		return
	}

	go func() {
		interval := 100 * time.Millisecond
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		tick := ticker.C
		done := bombardier.barrier.done()
		for {
			select {
			case <-tick:
				reqs := strconv.Itoa(int(bombardier.barrier.completedReqs()))
				c.WriteMessage(websocket.TextMessage, []byte(reqs))
				continue
			case <-done:
				bombardier.workers.Wait()
				reqs := strconv.Itoa(int(bombardier.barrier.completedReqs()))
				c.WriteMessage(websocket.TextMessage, []byte(reqs))
				bombardier.doneChan <- struct{}{}
				return
			}
		}
	}()
	bombardier.bombard()

	<-bombardier.doneChan

	resp := gatherInfo(bombardier)

	respData, err := json.Marshal(resp)
	if err != nil {
		return
	}

	c.WriteMessage(websocket.TextMessage, respData)
}

func main() {
	ln, err := net.Listen("tcp4", ":8081")
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(-5)
	}

	router := fasthttprouter.New()

	router.POST("/api/pt", requestHandling)

	router.GET("/ws", func(ctx *fasthttp.RequestCtx) {
		upgrader.Upgrade(ctx, webSocketRequestHandling)
	})

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		_ = ln.Close()
	}()
	_ = fasthttp.Serve(ln, router.Handler)
}
