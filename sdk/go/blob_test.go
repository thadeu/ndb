package ndb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
)

type Recording struct {
	CallID string `ndb:"_id"`
	Audio  Blob   `ndb:"audio"`
}

// A 10 MB value goes up from an io.Reader and comes back as a stream with
// the same bytes.
func TestBlobTenMegabyteRoundTrip(t *testing.T) {
	db := openTest(t, daemon.unix)

	data := make([]byte, 10<<20)

	rng := rand.New(rand.NewPCG(1, 2))

	for i := range data {
		data[i] = byte(rng.Uint32())
	}

	blob, err := db.PutBlob(context.Background(), bytes.NewReader(data), "audio/wav")
	if err != nil {
		t.Fatal(err)
	}

	if blob.Size != int64(len(data)) || blob.Type != "audio/wav" || !strings.HasPrefix(blob.Ref, "sha256:") {
		t.Fatalf("blob = %+v", blob)
	}

	body, err := db.GetBlob(context.Background(), blob)
	if err != nil {
		t.Fatal(err)
	}

	defer body.Close()

	got := sha256.New()

	if n, err := io.Copy(got, body); err != nil || n != int64(len(data)) {
		t.Fatalf("read %d bytes: %v", n, err)
	}

	want := sha256.Sum256(data)

	if !bytes.Equal(got.Sum(nil), want[:]) {
		t.Fatal("downloaded bytes differ")
	}
}

func TestBlobFieldInADocument(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	recs, err := Collection[Recording](ctx, db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	audio, err := db.PutBlob(ctx, strings.NewReader("RIFF-fake-wav"), "audio/wav")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := recs.Insert(ctx, Recording{CallID: "c1", Audio: audio}); err != nil {
		t.Fatal(err)
	}

	got, err := recs.Get(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}

	if got.Audio != audio {
		t.Fatalf("audio = %+v, want %+v", got.Audio, audio)
	}

	body, err := db.GetBlob(ctx, got.Audio)
	if err != nil {
		t.Fatal(err)
	}

	defer body.Close()

	data, _ := io.ReadAll(body)

	if string(data) != "RIFF-fake-wav" {
		t.Fatalf("data = %q", data)
	}
}

func TestGetBlobMissing(t *testing.T) {
	db := openTest(t, daemon.unix)

	_, err := db.GetBlob(context.Background(), Blob{Ref: "sha256:" + strings.Repeat("0", 64)})

	var apiErr *Error

	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("err = %v", err)
	}
}
