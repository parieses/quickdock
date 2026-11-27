package httpget

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type payload struct {
	Name string `json:"name"`
}

func retryClient(t *testing.T, handler http.Handler) (*http.Client, string, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	return &http.Client{Timeout: 2 * time.Second}, srv.URL, srv.Close
}

func TestGetJSONFirstSuccess(t *testing.T) {
	c, url, close := retryClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "test-agent" || r.Header.Get("X-Key") != "k" {
			t.Errorf("headers lost: %v", r.Header)
		}
		_, _ = w.Write([]byte(`{"name":"ok"}`))
	}))
	defer close()

	var out payload
	if err := GetJSON(context.Background(), url, &out, Options{
		Clients:   []*http.Client{c},
		UserAgent: "test-agent",
		Headers:   map[string]string{"X-Key": "k"},
	}); err != nil || out.Name != "ok" {
		t.Fatalf("got %+v err=%v", out, err)
	}
}

func TestGetJSONRetriesOnDecodeError(t *testing.T) {
	calls := 0
	c, url, close := retryClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`bad json`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"second"}`))
	}))
	defer close()

	var out payload
	if err := GetJSON(context.Background(), url, &out, Options{Clients: []*http.Client{c, c}}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || out.Name != "second" {
		t.Fatalf("calls=%d out=%+v", calls, out)
	}
}

func TestGetJSONStatusErrCallback(t *testing.T) {
	c, url, close := retryClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`invalid token`))
	}))
	defer close()

	type statusErr struct{ code int }
	var got statusErr
	var out payload
	err := GetJSON(context.Background(), url, &out, Options{
		Clients: []*http.Client{c},
		StatusErr: func(code int, body []byte) error {
			got = statusErr{code: code}
			return fmt.Errorf("status %d", code)
		},
	})
	if err == nil || got.code != 400 || err.Error() != "status 400" {
		t.Fatalf("err=%v got=%+v", err, got)
	}
}

func TestGetJSONCtxCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, url, close := retryClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called on canceled ctx")
	}))
	defer close()

	var out payload
	err := GetJSON(ctx, url, &out, Options{Clients: []*http.Client{c}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
