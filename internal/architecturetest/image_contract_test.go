package architecturetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerfileStampsEveryMetadataFieldIntoImageAndBinary(t *testing.T) {
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(data)

	for _, want := range []string{
		"ARG VERSION=dev",
		"ARG REVISION=unknown",
		"ARG BUILD_TIME=unknown",
		"ARG SOURCE=https://github.com/mfow/llm-temporal-worker",
		"ARG GO_VERSION=go1.26.0",
		"org.opencontainers.image.version=\"${VERSION}\"",
		"org.opencontainers.image.revision=\"${REVISION}\"",
		"org.opencontainers.image.created=\"${BUILD_TIME}\"",
		"org.opencontainers.image.source=\"${SOURCE}\"",
		"io.github.mfow.llm-temporal-worker.go.version=\"${GO_VERSION}\"",
		"-X github.com/mfow/llm-temporal-worker/internal/buildinfo.Version=${VERSION}",
		"-X github.com/mfow/llm-temporal-worker/internal/buildinfo.Revision=${REVISION}",
		"-X github.com/mfow/llm-temporal-worker/internal/buildinfo.BuildTime=${BUILD_TIME}",
		"-X github.com/mfow/llm-temporal-worker/internal/buildinfo.Source=${SOURCE}",
		"-X github.com/mfow/llm-temporal-worker/internal/buildinfo.GoVersion=${GO_VERSION}",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("Dockerfile does not stamp %q", want)
		}
	}
}

func TestImageVerifyTargetUsesHardenedRuntimeContract(t *testing.T) {
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(data)

	for _, want := range []string{
		"image-verify:",
		"docker build --tag",
		"git rev-parse HEAD",
		"LLMTW_IMAGE=",
		"-tags=imageintegration ./integration",
	} {
		if !strings.Contains(makefile, want) {
			t.Errorf("Makefile image-verify contract is missing %q", want)
		}
	}

	runtimeData, err := os.ReadFile(filepath.Join(root, "integration", "image_integration_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--read-only",
		"/tmp:rw,nosuid,nodev,noexec,size=64m",
		"--user",
		"65532:65532",
	} {
		if !strings.Contains(string(runtimeData), want) {
			t.Errorf("image runtime verification is missing %q", want)
		}
	}
}
