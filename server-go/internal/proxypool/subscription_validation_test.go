package proxypool

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateSubscriptionURL(t *testing.T) {
	valid := []string{
		"https://example.com/sub?token=abc",
		"http://127.0.0.1:8080/sub",
		"https://user:pass@example.com:443/sub",
	}
	for _, raw := range valid {
		if err := ValidateSubscriptionURL(raw); err != nil {
			t.Errorf("ValidateSubscriptionURL(%q): %v", raw, err)
		}
	}
	invalid := []string{
		"", "%", "example.com/sub", "ftp://example.com/sub", "https:///sub",
		"https://example.com/a b", "https://example.com/%zz", "http://example.com:0/sub", "http://example.com:65536/sub",
	}
	for _, raw := range invalid {
		if err := ValidateSubscriptionURL(raw); err == nil {
			t.Errorf("ValidateSubscriptionURL(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestFetchURLInvalidURL(t *testing.T) {
	r := FetchURL("%", 0, 100)
	if r.Error == "" || !strings.Contains(r.Error, "URL 格式错误") {
		t.Fatalf("FetchURL invalid URL = %+v", r)
	}
}

func TestFetchURLLimitsResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxFetchBodyBytes+1)))
	}))
	defer srv.Close()
	r := FetchURL(srv.URL, 0, 5_000)
	if r.Error == "" || !strings.Contains(r.Error, "8MB") {
		t.Fatalf("oversize response = %+v", r)
	}
	if len(r.Body) > maxFetchBodyBytes+1 {
		t.Fatalf("response body was not bounded: %d bytes", len(r.Body))
	}
}

func TestLoadSubscriptionRejectsInvalidURL(t *testing.T) {
	if _, err := LoadSubscription("%", nil); err == nil {
		t.Fatal("LoadSubscription accepted an invalid URL")
	}
}
