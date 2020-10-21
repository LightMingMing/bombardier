package main

import (
	"reflect"
	"testing"
)

func Test_jsonPathAssert(t *testing.T) {
	type args struct {
		data      []byte
		assertion assertion
	}
	tests := []struct {
		name string
		args args
		want assertResult
	}{
		{
			name: "Not null",
			args: args{
				data: []byte("{\"user\": {\"name\":\"Tom\"}}"),
				assertion: assertion{
					asserter:   "JsonPath",
					expression: "$.user",
					condition:  "NOT_NULL",
				},
			},
			want: success,
		}, {
			name: "Is null",
			args: args{
				data: []byte("{\"user\": {\"name\":\"Tom\"}}"),
				assertion: assertion{
					asserter:   "JsonPath",
					expression: "$.age",
					condition:  "NULL",
				},
			},
			want: success,
		},
		{
			name: "Equal",
			args: args{
				data: []byte("{\"user\": {\"name\":\"Tom\"}}"),
				assertion: assertion{
					asserter:   "JsonPath",
					expression: "$.user.name",
					condition:  "EQUAL",
					expected:   "Tom",
				},
			},
			want: success,
		},
		{
			name: "String Not Equal",
			args: args{
				data: []byte("{\"user\": {\"name\":\"Tom\"}}"),
				assertion: assertion{
					asserter:   "JsonPath",
					expression: "$.user",
					condition:  "EQUAL",
					expected:   "Tom",
				},
			},
			want: failure,
		},
		{
			name: "Int Equal",
			args: args{
				data: []byte("{\"user\": {\"age\":20}}"),
				assertion: assertion{
					asserter:   "JsonPath",
					expression: "$.user.age",
					condition:  "EQUAL",
					expected:   "20",
				},
			},
			want: success,
		},
		{
			name: "Float Equal",
			args: args{
				data: []byte("{\"user\": {\"deposit\":100.05}}"),
				assertion: assertion{
					asserter:   "JsonPath",
					expression: "$.user.deposit",
					condition:  "EQUAL",
					expected:   "100.05",
				},
			},
			want: success,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jsonPathAssert(tt.args.data, tt.args.assertion); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("jsonPathAssert() = %v, want %v", got, tt.want)
			}
		})
	}
}
