package main

import (
	"testing"
)

func TestGetKeys(t *testing.T) {
	ok := containsPlaceholder("Hello, ${name}")

	if !ok {
		t.Errorf("Expected \"%v\", but got \"%v\"", true, ok)
	}
}

func TestReplace(t *testing.T) {
	vars := map[string]string{"name": "bombardier"}
	actual := replace("Hello, ${name}", vars)
	expected := "Hello, bombardier"
	if actual != expected {
		t.Errorf("Expected \"%v\", but got \"%v\"", expected, actual)
	}
}
