package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/mfow/llm-temporal-worker/config"
)

type fakeClients struct {
	mu       sync.Mutex
	closed   int
	closeErr error
}

func (clients *fakeClients) Close(context.Context) error {
	clients.mu.Lock()
	defer clients.mu.Unlock()
	clients.closed++
	return clients.closeErr
}

func (clients *fakeClients) Count() int {
	clients.mu.Lock()
	defer clients.mu.Unlock()
	return clients.closed
}

func TestReloadRejectsInvalidConfigAndDrainsOldClients(t *testing.T) {
	initial := exampleConfig(t)
	created := make(chan *fakeClients, 2)
	application, err := New(context.Background(), Options{
		InitialConfig: initial,
		Builder:       SnapshotBuilder{},
		Clients: func(context.Context, *config.Snapshot) (ClientSet, error) {
			clients := &fakeClients{}
			created <- clients
			return clients, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	oldClients := <-created
	oldVersion := application.Current().Config.ConfigVersion()
	if err := application.Reload(context.Background(), []byte("version: invalid\n")); err == nil {
		t.Fatal("invalid reload unexpectedly succeeded")
	}
	if application.Current().Config.ConfigVersion() != oldVersion {
		t.Fatal("invalid reload replaced the active snapshot")
	}
	old := application.Current()
	lease, err := application.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	reloaded := make(chan error, 1)
	go func() { reloaded <- application.Reload(context.Background(), initial) }()
	newClients := <-created
	if oldClients.Count() != 0 {
		t.Fatal("old clients closed while an Activity lease was active")
	}
	lease.Release()
	if err := <-reloaded; err != nil {
		t.Fatal(err)
	}
	if err := old.WaitClosed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if oldClients.Count() != 1 || newClients.Count() != 1 {
		t.Fatalf("closed counts = %d, %d; want one each", oldClients.Count(), newClients.Count())
	}
}

func TestReloadClientConstructionFailureKeepsOldSnapshot(t *testing.T) {
	failure := errors.New("client construction failed")
	count := 0
	application, err := New(context.Background(), Options{
		InitialConfig: exampleConfig(t), Builder: SnapshotBuilder{},
		Clients: func(context.Context, *config.Snapshot) (ClientSet, error) {
			count++
			if count > 1 {
				return nil, failure
			}
			return ClientSetFunc(func(context.Context) error { return nil }), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	before := application.Current().Config.ConfigVersion()
	if err := application.Reload(context.Background(), exampleConfig(t)); !errors.Is(err, failure) {
		t.Fatalf("reload error = %v", err)
	}
	if application.Current().Config.ConfigVersion() != before {
		t.Fatal("client construction failure replaced active snapshot")
	}
}
