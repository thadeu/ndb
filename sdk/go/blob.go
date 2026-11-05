package ndb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
)

// Blob is a reference to a large value in the blob store (plan §7.5). Use it
// as a struct field: it is stored in the document as
// {"$blob":"sha256:...","size":N,"type":"..."}, and the content stays out of
// the document. Get a Blob from DB.PutBlob and read the content with
// DB.GetBlob.
type Blob struct {
	Ref  string `json:"$blob"`
	Size int64  `json:"size"`
	Type string `json:"type,omitempty"`
}

// PutBlob streams r to the daemon and returns the reference. The same
// content always gives the same reference. contentType is kept in the
// reference.
func (db *DB) PutBlob(ctx context.Context, r io.Reader, contentType string) (Blob, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, db.base+"/v1/blobs", r)
	if err != nil {
		return Blob{}, err
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := db.client.Do(req)
	if err != nil {
		return Blob{}, err
	}

	if resp.StatusCode >= http.StatusMultipleChoices {
		return Blob{}, apiError(resp)
	}

	defer resp.Body.Close()

	var b Blob

	return b, json.NewDecoder(resp.Body).Decode(&b)
}

// GetBlob opens the content of b as a stream. The caller closes it.
func (db *DB) GetBlob(ctx context.Context, b Blob) (io.ReadCloser, error) {
	path := "/v1/blobs/" + url.PathEscape(b.Ref)

	if b.Type != "" {
		path += "?type=" + url.QueryEscape(b.Type)
	}

	resp, err := db.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	return resp.Body, nil
}
