package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadAllWithLimit(t *testing.T) {
	data, err := readAllWithLimit(strings.NewReader("hello"), 5)
	if err != nil || string(data) != "hello" {
		t.Fatalf("exact limit: got (%q, %v), want (\"hello\", nil)", string(data), err)
	}
	if _, err := readAllWithLimit(strings.NewReader("hello!"), 5); err == nil || !strings.Contains(err.Error(), "response body exceeds") {
		t.Fatalf("exceeded limit: err = %v, want response body exceeds error", err)
	}
	unlimited, err := readAllWithLimit(strings.NewReader("unlimited"), 0)
	if err != nil || string(unlimited) != "unlimited" {
		t.Fatalf("zero limit: got (%q, %v), want (\"unlimited\", nil)", string(unlimited), err)
	}
}

func TestCapReader(t *testing.T) {
	cr := &capReader{r: strings.NewReader("12345"), limit: 5}
	got, err := io.ReadAll(cr)
	if err != nil || string(got) != "12345" {
		t.Fatalf("exact cap: got (%q, %v), want (\"12345\", nil)", string(got), err)
	}
	over := &capReader{r: strings.NewReader("123456"), limit: 5}
	if _, err := io.ReadAll(over); err == nil || !strings.Contains(err.Error(), "response body exceeds") {
		t.Fatalf("over cap: err = %v, want response body exceeds error", err)
	}
}

func TestSendNotifyRequestRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxNotifyResponseBytes+1)))
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	sendErr := sendNotifyRequest(server.Client(), req)
	if sendErr == nil {
		t.Fatalf("sendNotifyRequest() error = nil, want response size error")
	}
	if !strings.Contains(strings.ToLower(sendErr.Error()), "response body exceeds") {
		t.Fatalf("sendNotifyRequest() error = %q, want response size error", sendErr)
	}
}
