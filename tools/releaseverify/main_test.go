package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestResolveOCIManifestDescriptorRejectsCyclicOCIIndexChain(t *testing.T) {
	const (
		digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		digestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	indexA := []byte(fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"mediaType":"%s","digest":"sha256:%s","size":999}]}`,
		ociImageIndexMediaType, digestB))
	indexB := []byte(fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"mediaType":"%s","digest":"sha256:%s","size":999}]}`,
		ociImageIndexMediaType, digestA))
	if len(indexA) > 999 || len(indexB) > 999 {
		t.Fatal("test index payload no longer fits the stable three-digit size placeholder")
	}
	indexA = []byte(fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"mediaType":"%s","digest":"sha256:%s","size":%d}]}`,
		ociImageIndexMediaType, digestB, len(indexB)))
	indexB = []byte(fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"mediaType":"%s","digest":"sha256:%s","size":%d}]}`,
		ociImageIndexMediaType, digestA, len(indexA)))
	entries := map[string]ociLayoutEntry{
		"blobs/sha256/" + digestA: {size: int64(len(indexA)), digest: digestA, data: indexA},
		"blobs/sha256/" + digestB: {size: int64(len(indexB)), digest: digestB, data: indexB},
	}

	_, err := resolveOCIManifestDescriptor(entries, ociDescriptor{
		MediaType: ociImageIndexMediaType,
		Digest:    "sha256:" + digestA,
		Size:      int64(len(indexA)),
	}, map[string]struct{}{})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cyclic OCI index chain error = %v, want cycle rejection", err)
	}
}
