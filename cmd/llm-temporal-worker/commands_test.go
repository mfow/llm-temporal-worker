package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exampleConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "config.example.yaml")
}

func TestConfigCommandsDoNotPrintSecretValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data, err := os.ReadFile(exampleConfigPath(t))
	if err != nil {
		t.Fatal(err)
	}
	secret := "adversarial-secret-value"
	data = append(data, []byte("\n# "+secret+"\n")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"validate-config", "--config", path}, {"print-effective-config", "--config", path}} {
		var output, errorsOut bytes.Buffer
		code := Execute(context.Background(), command, CommandOptions{Out: &output, ErrOut: &errorsOut})
		if code != 0 {
			t.Fatalf("command %v code=%d error=%s", command, code, errorsOut.String())
		}
		if strings.Contains(output.String(), secret) || strings.Contains(errorsOut.String(), secret) {
			t.Fatalf("command %v leaked secret: out=%q err=%q", command, output.String(), errorsOut.String())
		}
	}
}

func TestWorkerAndReconcileCommandsUseScopedDependencies(t *testing.T) {
	path := exampleConfigPath(t)
	workerCalled := false
	reconcileCalled := ""
	var output, errorsOut bytes.Buffer
	code := Execute(context.Background(), []string{"worker", "--config", path}, CommandOptions{
		Out: &output, ErrOut: &errorsOut,
		RunWorker: func(_ context.Context, data []byte, _ io.Writer) error {
			workerCalled = len(data) > 0
			return nil
		},
	})
	if code != 0 || !workerCalled {
		t.Fatalf("worker code=%d called=%v errors=%s", code, workerCalled, errorsOut.String())
	}
	code = Execute(context.Background(), []string{"reconcile", "--operation-id", "op-safe"}, CommandOptions{
		Out: &output, ErrOut: &errorsOut,
		Reconcile: func(_ context.Context, operationID string) error { reconcileCalled = operationID; return nil },
	})
	if code != 0 || reconcileCalled != "op-safe" || !strings.Contains(output.String(), "reconcile complete") {
		t.Fatalf("reconcile code=%d id=%q output=%q", code, reconcileCalled, output.String())
	}
}

func TestCommandFailuresAreRedacted(t *testing.T) {
	var output, errorsOut bytes.Buffer
	code := Execute(context.Background(), []string{"reconcile", "--operation-id", "op-safe"}, CommandOptions{
		Out: &output, ErrOut: &errorsOut,
		Reconcile: func(context.Context, string) error { return errors.New("secret-token-value") },
	})
	if code != 1 || strings.Contains(errorsOut.String(), "secret-token-value") {
		t.Fatalf("failure code=%d output=%q", code, errorsOut.String())
	}
}
