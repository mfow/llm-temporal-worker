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

func TestImageVerifyOCIExportUsesOneSupportedBuildxSolve(t *testing.T) {
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(data)

	start := strings.Index(makefile, `if [ -n "$(IMAGE_VERIFY_OCI_LAYOUT)" ]; then`)
	if start < 0 {
		t.Fatal("Makefile is missing the OCI layout image-verify branch")
	}
	end := strings.Index(makefile[start:], "\t\telse \\")
	if end < 0 {
		t.Fatal("Makefile OCI layout image-verify branch is missing its fallback boundary")
	}
	branch := makefile[start : start+end]

	for _, want := range []string{
		"docker buildx build --platform linux/amd64 --provenance=false --sbom=false",
		`--output "type=oci,dest=$$layout,tar=false,name=$(IMAGE_VERIFY_TAG)"`,
		"--load \\",
		`docker image inspect "$(IMAGE_VERIFY_TAG)"`,
	} {
		if !strings.Contains(branch, want) {
			t.Fatalf("OCI layout image-verify branch is missing %q", want)
		}
	}
	if strings.Count(branch, "docker buildx build") != 1 {
		t.Fatalf("OCI layout image-verify branch must use exactly one Buildx solve: %q", branch)
	}
	if strings.Contains(branch, "docker load --input") {
		t.Fatal("OCI layout image-verify branch must not load an OCI directory through docker load")
	}
}
