package s3blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/mfow/llm-temporal-worker/storage/blob"
)

type fakeS3 struct {
	putInput        *s3.PutObjectInput
	getInput        *s3.GetObjectInput
	bucketHeadInput *s3.HeadBucketInput
	putErr          error
	getErr          error
	bucketHeadErr   error
	headCalls       int
	bucketHeadCalls int
	data            []byte
}

func (fake *fakeS3) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	fake.putInput = input
	return &s3.PutObjectOutput{}, fake.putErr
}

func (fake *fakeS3) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	fake.getInput = input
	if fake.getErr != nil {
		return nil, fake.getErr
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(fake.data)), ContentLength: int64Ptr(int64(len(fake.data)))}, nil
}

func (fake *fakeS3) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	fake.headCalls++
	return &s3.HeadObjectOutput{}, nil
}

func (fake *fakeS3) HeadBucket(_ context.Context, input *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	fake.bucketHeadCalls++
	fake.bucketHeadInput = input
	return &s3.HeadBucketOutput{}, fake.bucketHeadErr
}

func int64Ptr(value int64) *int64 { return &value }

func TestStoreUsesContentAddressedConditionalS3Object(t *testing.T) {
	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	fake := &fakeS3{data: []byte("payload")}
	store, err := New(Options{Client: fake, Bucket: "bucket", Prefix: "v1", MaxBytes: 100, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put(context.Background(), blob.PutRequest{Tenant: "tenant", MediaType: "text/plain", Data: []byte("payload"), ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if fake.putInput == nil || fake.putInput.IfNoneMatch == nil || *fake.putInput.IfNoneMatch != "*" {
		t.Fatal("PutObject did not request create-if-absent")
	}
	got, err := store.Get(context.Background(), "tenant", ref)
	if err != nil || string(got) != "payload" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if fake.getInput == nil || *fake.getInput.Key != ref.Locator {
		t.Fatal("GetObject used an unexpected key")
	}
}

func TestStoreHandlesExistingAndDigestMismatch(t *testing.T) {
	now := time.Now().UTC()
	fake := &fakeS3{putErr: &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "exists"}}
	store, err := New(Options{Client: fake, Bucket: "bucket", Prefix: "v1", MaxBytes: 100, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Put(context.Background(), blob.PutRequest{Tenant: "tenant", MediaType: "text/plain", Data: []byte("payload"), ExpiresAt: now.Add(time.Hour)})
	if err != nil || fake.headCalls != 1 {
		t.Fatalf("existing put = %v, head calls %d", err, fake.headCalls)
	}
	fake.data = []byte("other")
	ref := blob.Ref{Store: "s3", Locator: "v1/" + "bad", Digest: blob.Digest([]byte("payload")), ByteLength: 7, MediaType: "text/plain", ExpiresAt: now.Add(time.Hour)}
	if _, err := store.Get(context.Background(), "tenant", ref); !errors.Is(err, blob.ErrTenantMismatch) {
		t.Fatalf("wrong locator error = %v", err)
	}
}

func TestProbeBucketUsesOnlyBucketMetadata(t *testing.T) {
	fake := &fakeS3{}
	store, err := New(Options{Client: fake, Bucket: "bucket", Prefix: "v1", MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProbeBucket(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.bucketHeadCalls != 1 || fake.bucketHeadInput == nil || fake.bucketHeadInput.Bucket == nil || *fake.bucketHeadInput.Bucket != "bucket" {
		t.Fatalf("HeadBucket calls/input = %d/%#v", fake.bucketHeadCalls, fake.bucketHeadInput)
	}
	if fake.putInput != nil || fake.getInput != nil || fake.headCalls != 0 {
		t.Fatal("bucket probe accessed tenant object content")
	}
}
