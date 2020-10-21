package main

import (
	"encoding/json"
	"github.com/oliveagle/jsonpath"
	"strconv"
)

type assertion struct {
	asserter   string
	expression string
	condition  string
	expected   string
}

type assertResult struct {
	successful bool
	errMsg     string
}

var success = assertResult{successful: true}

var failure = assertResult{successful: false}

func jsonPathAssert(data []byte, assertion assertion) assertResult {
	// TODO
	var jsonData interface{}
	err := json.Unmarshal(data, &jsonData)
	if err != nil {
		return failure
	}

	res, err := jsonpath.JsonPathLookup(jsonData, assertion.expression)

	if "NULL" == assertion.condition {
		if res == nil {
			return success
		} else {
			return failure
		}
	}

	if "NOT_NULL" == assertion.condition {
		if res == nil {
			return failure
		} else {
			return success
		}
	}

	if "EQUAL" == assertion.condition {
		if res == nil {
			return failure
		}

		var actual string
		switch res.(type) {
		case float64:
			actual = strconv.FormatFloat(res.(float64), 'f', -1, 64)
		case int:
			actual = strconv.Itoa(res.(int))
		case string:
			actual = res.(string)
		}
		if actual == assertion.expected {
			return success
		} else {
			return failure
		}
	}

	return success
}

func assertThat(data []byte, assertions []assertion) assertResult {
	for _, assertion := range assertions {
		if "JsonPath" == assertion.asserter {
			r := jsonPathAssert(data, assertion)
			if !r.successful {
				return r
			}
		}
	}
	return success
}
