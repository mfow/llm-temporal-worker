package httpserver_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mfow/llm-temporal-worker/golang/internal/httpserver"
)

func TestProbeStateFailsClosedAndDoesNotExposeDetails(t *testing.T) {
	state := httpserver.NewHealthState()
	handler := httpserver.Handler(state, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("metric-value\n"))
	}))

	check := func(path string, status int, body string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s status = %d, want %d", path, response.Code, status)
		}
		data, err := io.ReadAll(response.Result().Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != body {
			t.Fatalf("%s body = %q, want %q", path, data, body)
		}
	}

	check(httpserver.LivePath, http.StatusOK, "ok\n")
	check(httpserver.ReadyPath, http.StatusServiceUnavailable, "not ready\n")
	state.SetReady(true)
	check(httpserver.ReadyPath, http.StatusOK, "ok\n")
	state.SetLive(false)
	check(httpserver.LivePath, http.StatusServiceUnavailable, "not live\n")
	check(httpserver.MetricsPath, http.StatusOK, "metric-value\n")
}

func TestServerBindsAndShutsDown(t *testing.T) {
	state := httpserver.NewHealthState()
	server, err := httpserver.New(httpserver.Options{Address: "127.0.0.1:0", Health: state})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skip("sandbox does not permit binding a TCP listener")
		}
		t.Fatal(err)
	}
	if server.Addr() == "" {
		t.Fatal("server did not expose a bound address")
	}
	if err := server.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
