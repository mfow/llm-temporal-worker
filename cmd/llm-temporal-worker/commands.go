package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mfow/llm-temporal-worker/config"
)

const defaultConfigPath = "/etc/llmtw/config.yaml"

var errWorkerRuntimeUnavailable = errors.New("worker runtime dependencies are unavailable")

type CommandOptions struct {
	Out       io.Writer
	ErrOut    io.Writer
	Resolver  config.ReferenceResolver
	RunWorker func(context.Context, []byte, io.Writer) error
	Reconcile func(context.Context, string) error
}

func Execute(ctx context.Context, args []string, options CommandOptions) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Out == nil {
		options.Out = io.Discard
	}
	if options.ErrOut == nil {
		options.ErrOut = io.Discard
	}
	if len(args) == 0 {
		writeCommandError(options.ErrOut, errors.New("a command is required"))
		return 2
	}
	switch args[0] {
	case "validate-config":
		return executeConfigCommand(ctx, args[1:], options, false)
	case "print-effective-config":
		return executeConfigCommand(ctx, args[1:], options, true)
	case "worker":
		return executeWorkerCommand(ctx, args[1:], options)
	case "reconcile":
		return executeReconcileCommand(ctx, args[1:], options)
	case "help", "-h", "--help":
		writeUsage(options.Out)
		return 0
	default:
		writeCommandError(options.ErrOut, fmt.Errorf("unknown command %q", args[0]))
		return 2
	}
}

func executeConfigCommand(ctx context.Context, args []string, options CommandOptions, printEffective bool) int {
	flags := flag.NewFlagSet("config", flag.ContinueOnError)
	flags.SetOutput(options.ErrOut)
	path := flags.String("config", defaultConfigPath, "configuration YAML path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	data, err := readConfig(*path)
	if err != nil {
		writeCommandError(options.ErrOut, err)
		return 1
	}
	snapshot, err := config.Compile(ctx, data, options.Resolver)
	if err != nil {
		writeCommandError(options.ErrOut, err)
		return 1
	}
	if printEffective {
		_, _ = options.Out.Write(snapshot.Canonical())
		_, _ = io.WriteString(options.Out, "\n")
		return 0
	}
	_, _ = fmt.Fprintf(options.Out, "valid config version %s\n", snapshot.ConfigVersion())
	return 0
}

func executeWorkerCommand(ctx context.Context, args []string, options CommandOptions) int {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	flags.SetOutput(options.ErrOut)
	path := flags.String("config", defaultConfigPath, "configuration YAML path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	data, err := readConfig(*path)
	if err != nil {
		writeCommandError(options.ErrOut, err)
		return 1
	}
	if _, err := config.Compile(ctx, data, options.Resolver); err != nil {
		writeCommandError(options.ErrOut, err)
		return 1
	}
	if options.RunWorker == nil {
		writeCommandError(options.ErrOut, errWorkerRuntimeUnavailable)
		return 1
	}
	if err := options.RunWorker(ctx, data, options.Out); err != nil {
		writeCommandError(options.ErrOut, err)
		return 1
	}
	return 0
}

func executeReconcileCommand(ctx context.Context, args []string, options CommandOptions) int {
	flags := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	flags.SetOutput(options.ErrOut)
	operationID := flags.String("operation-id", "", "safe operation ID")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *operationID == "" || strings.ContainsAny(*operationID, "\r\n") || len(*operationID) > 256 {
		writeCommandError(options.ErrOut, errors.New("operation-id is required"))
		return 2
	}
	if options.Reconcile == nil {
		writeCommandError(options.ErrOut, errors.New("reconcile backend is unavailable"))
		return 1
	}
	if err := options.Reconcile(ctx, *operationID); err != nil {
		writeCommandError(options.ErrOut, err)
		return 1
	}
	_, _ = io.WriteString(options.Out, "reconcile complete\n")
	return 0
}

func readConfig(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("config path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read configuration: %w", err)
	}
	defer file.Close()
	const maxBytes = 4 << 20
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, errors.New("read configuration failed")
	}
	if len(data) > maxBytes {
		return nil, errors.New("configuration exceeds safe size")
	}
	return data, nil
}

func writeCommandError(output io.Writer, err error) {
	if output == nil {
		return
	}
	message := "command failed"
	if err != nil {
		candidate := strings.TrimSpace(strings.SplitN(err.Error(), "\n", 2)[0])
		lower := strings.ToLower(candidate)
		for _, word := range []string{"secret", "password", "token", "authorization", "credential", "prompt", "output", "provider body"} {
			if strings.Contains(lower, word) {
				candidate = "command failed"
				break
			}
		}
		if len(candidate) > 240 {
			candidate = "command failed"
		}
		if candidate != "" {
			message = candidate
		}
	}
	_, _ = fmt.Fprintf(output, "%s\n", message)
}

func writeUsage(output io.Writer) {
	_, _ = io.WriteString(output, "usage: llm-temporal-worker <worker|validate-config|print-effective-config|reconcile>\n")
}
