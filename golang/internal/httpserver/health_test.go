package httpserver_test

import (
	"io"
	"net"
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

func TestHandlerDefaultsAreSafeAndExposeOnlyProbeResponses(t *testing.T) {
	handler := httpserver.Handler(nil, nil)

	check := func(path string, status int, body string) {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
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
	check(httpserver.MetricsPath, http.StatusNotFound, "404 page not found\n")
	check("/unexpected", http.StatusNotFound, "404 page not found\n")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, httpserver.LivePath, nil))
	if got := response.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("probe content type = %q, want text/plain; charset=utf-8", got)
	}
}

func TestNewRequiresAddress(t *testing.T) {
	if _, err := httpserver.New(httpserver.Options{}); err == nil {
		t.Fatal("New accepted an empty address")
	}
}

func TestServerNilAndDuplicateStartErrors(t *testing.T) {
	var nilServer *httpserver.Server
	if err := nilServer.Start(); err == nil {
		t.Fatal("nil server Start returned nil")
	}
	if nilServer.Addr() != "" {
		t.Fatalf("nil server Addr = %q, want empty", nilServer.Addr())
	}
	if nilServer.Errors() != nil {
		t.Fatal("nil server Errors returned a channel")
	}
	if err := nilServer.Shutdown(t.Context()); err != nil {
		t.Fatalf("nil server Shutdown() error = %v", err)
	}

	server, err := httpserver.New(httpserver.Options{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown before Start() error = %v", err)
	}
	if err := server.Start(); err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skip("sandbox does not permit binding a TCP listener")
		}
		t.Fatal(err)
	}
	defer server.Shutdown(t.Context())
	if err := server.Start(); err == nil {
		t.Fatal("duplicate Start returned nil")
	}
}

func TestServerReportsBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skip("sandbox does not permit binding a TCP listener")
		}
		t.Fatal(err)
	}
	defer listener.Close()

	server, err := httpserver.New(httpserver.Options{Address: listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err == nil || !strings.Contains(err.Error(), "listen for health server") {
		t.Fatalf("Start() error = %v, want wrapped listen error", err)
	}
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
	client := &http.Client{}
	response, err := client.Get("http://" + server.Addr() + httpserver.LivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("live probe status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok\n" {
		t.Fatalf("live probe body = %q, want %q", body, "ok\n")
	}
	if err := server.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
