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

func TestImageVerifyOCIArchiveUsesOneSupportedBuildxSolve(t *testing.T) {
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
		`--output "type=docker,oci-mediatypes=true,dest=$$archive,tar=true,name=$(IMAGE_VERIFY_TAG)"`,
		`archive_directory="$$(mktemp -d "$${TMPDIR:-/tmp}/llmtw-image-verify.XXXXXX")"`,
		`cleanup_archive() { rm -rf -- "$$archive_directory"; };`,
		`docker image load --input "$$archive"`,
		`tar -xf "$$archive" -C "$$layout"`,
		`docker image inspect "$(IMAGE_VERIFY_TAG)"`,
		"trap cleanup_archive EXIT HUP INT TERM",
	} {
		if !strings.Contains(branch, want) {
			t.Fatalf("OCI layout image-verify branch is missing %q", want)
		}
	}
	if strings.Count(branch, "docker buildx build") != 1 {
		t.Fatalf("OCI layout image-verify branch must use exactly one Buildx solve: %q", branch)
	}
	if strings.Count(branch, "--output") != 1 {
		t.Fatalf("OCI layout image-verify branch must use exactly one Buildx exporter: %q", branch)
	}
	for _, forbidden := range []string{
		"--load",
		`docker image load --input "$$layout"`,
		`--output "type=oci,`,
		`rm -rf -- "$$layout"`,
	} {
		if strings.Contains(branch, forbidden) {
			t.Fatalf("OCI layout image-verify branch must not retain competing or directory loading behavior %q", forbidden)
		}
	}
	build := strings.Index(branch, "docker buildx build")
	load := strings.Index(branch, `docker image load --input "$$archive"`)
	extract := strings.Index(branch, `tar -xf "$$archive" -C "$$layout"`)
	if build < 0 || load <= build || extract <= load {
		t.Fatalf("OCI archive must be built once, then loaded and extracted from that exact artifact: %q", branch)
	}
}
