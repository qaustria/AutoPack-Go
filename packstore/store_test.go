package packstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRetainsEveryPortAndIndexesLatestByPackID(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "cone.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first, err := store.Save(context.Background(), Record{
		PackID: "abc123", PackName: "first.zip", OutputJSON: []byte(`{"version":1}`),
		CreatedAt: time.Unix(100, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Save(context.Background(), Record{
		PackID: "abc123", PackName: "second.zip", OutputJSON: []byte(`{"version":2}`),
		CreatedAt: time.Unix(200, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("sequences = %d, %d; want 1, 2", first.Sequence, second.Sequence)
	}
	count, err := store.Count(context.Background())
	if err != nil || count != 2 {
		t.Fatalf("count = %d, %v; want 2", count, err)
	}
	latest, found, err := store.Latest(context.Background(), "abc123")
	if err != nil || !found || latest.Sequence != second.Sequence || latest.PackName != "second.zip" {
		t.Fatalf("latest = %#v, found=%v, err=%v", latest, found, err)
	}
}

func TestStoreRejectsInvalidRecords(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "cone.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Save(context.Background(), Record{PackID: "id", PackName: "pack.zip", OutputJSON: []byte("not json")}); err == nil {
		t.Fatal("invalid output JSON was accepted")
	}
}
