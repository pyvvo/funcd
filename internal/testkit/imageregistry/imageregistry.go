// Package imageregistry serves one image, read from a `docker save` tar whose OCI layout holds it, gzipped or not, as a
// read-only OCI registry: the addon-runtime lane's (e2e/laneregistry, ADR-0344 Decision 5), and that of funcdctl dev's
// worker scenarios, on an engine that keeps a digest only for an image it pulled (ADR-0346).
package imageregistry

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Image is an OCI layout read into memory: its blobs by digest, and the descriptor of index.json's first manifest.
type Image struct {
	blobs map[string][]byte
	top   descriptor
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
}

// Read reads every blob and index.json of the tar r.
func Read(r io.Reader) (*Image, error) {
	br := bufio.NewReader(r)
	var src io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		src = zr
	}
	img := &Image{blobs: map[string][]byte{}}
	var index []byte
	tr := tar.NewReader(src)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(h.Name, "./")
		hex, isBlob := strings.CutPrefix(name, "blobs/sha256/")
		if name != "index.json" && !isBlob {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		if isBlob {
			img.blobs["sha256:"+hex] = b
		} else {
			index = b
		}
	}
	var idx struct {
		Manifests []descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(index, &idx); err != nil || len(idx.Manifests) == 0 {
		return nil, errors.New("the archive holds no OCI index.json with a manifest")
	}
	img.top = idx.Manifests[0]
	return img, nil
}

// Digest is the digest of index.json's first manifest, the one a pull names.
func (img *Image) Digest() string { return img.top.Digest }

// Handler serves the distribution API's reads of repository: /v2/, its manifests by digest or by the tag latest, and
// its blobs by digest.
func (img *Image) Handler(repository string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		rest, ok := strings.CutPrefix(r.URL.Path, "/v2/"+repository+"/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch kind, ref, _ := strings.Cut(rest, "/"); kind {
		case "manifests":
			if ref == "latest" {
				ref = img.top.Digest
			}
			img.serve(w, r, ref, img.mediaType(ref))
		case "blobs":
			img.serve(w, r, ref, "application/octet-stream")
		default:
			http.NotFound(w, r)
		}
	})
}

// mediaType is the media type of the manifest or index of digest: its own mediaType field, else the index's.
func (img *Image) mediaType(digest string) string {
	var m struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(img.blobs[digest], &m); err == nil && m.MediaType != "" {
		return m.MediaType
	}
	return img.top.MediaType
}

func (img *Image) serve(w http.ResponseWriter, r *http.Request, digest, mediaType string) {
	b, ok := img.blobs[digest]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(b)
	}
}
