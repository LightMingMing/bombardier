package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type payload struct {
	data      []map[string]string
	readCount uint32
	len       uint32
}

func loadFromFile(filePath string, columns []string, startLine uint32) (*payload, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	reader := csv.NewReader(file)

	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if records == nil {
		return nil, errors.New("file content is empty")
	}

	colLen := len(columns)
	rowLen := len(records)

	if colLen != len(records[0]) {
		return nil, errors.New("number of variables does not match the number of csv file columns")
	}

	data := make([]map[string]string, len(records))
	for row := 0; row < rowLen; row++ {
		data[row] = map[string]string{}
		for col := 0; col < colLen; col++ {
			data[row][columns[col]] = records[row][col]
		}
	}

	return &payload{
		data:      data,
		readCount: startLine,
		len:       uint32(rowLen),
	}, nil

}

func loadFromUrl(payloadUrl string, columns []string, offset uint32, limit uint32) (*payload, error) {
	params := url.Values{}
	params.Set("columns", strings.Join(columns, ","))
	params.Set("offset", strconv.Itoa(int(offset)))
	params.Set("limit", strconv.Itoa(int(limit)))

	reqUrl, _ := url.Parse(payloadUrl)
	reqUrl.RawQuery = params.Encode()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(reqUrl.String())
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, _ := ioutil.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		if body != nil {
			return nil, errors.New(string(body))
		} else {
			return nil, errors.New("invalid status code '" + strconv.Itoa(resp.StatusCode) + "'")
		}
	}

	data := make([]map[string]string, limit)
	err = json.Unmarshal(body, &data)
	if err != nil {
		return nil, err
	}

	return &payload{
		data:      data,
		readCount: 0,
		len:       limit,
	}, nil
}

func (payload *payload) next() map[string]string {
	var prev uint32
	var next uint32
	for {
		prev = payload.readCount
		next = prev + 1
		if atomic.CompareAndSwapUint32(&payload.readCount, prev, next) {
			return payload.data[prev%payload.len]
		}
	}
}

func (payload *payload) get(s scope, idx uint64) map[string]string {
	if s == request {
		return payload.next()
	} else if s == thread {
		return payload.data[(payload.readCount+uint32(idx))%payload.len]
	} else {
		return payload.data[payload.readCount%payload.len]
	}
}
