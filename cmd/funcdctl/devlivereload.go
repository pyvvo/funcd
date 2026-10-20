//go:build dev

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// devLiveReload serves the reload events of ADR-0255 Decision 5 on a loopback port kept in dev's process dir, and tells
// open Site pages to reload when a revision dev applied serves new Site builds.
type devLiveReload struct {
	port  int
	srv   *http.Server
	mu    sync.Mutex
	subs  map[chan struct{}]struct{}
	sites map[v1.ObjectName]string // each Site part's image at the last event, or when the stream started
}

// startLiveReload serves the stream on the port processDir's livereload.port keeps, or on a free one it then keeps
// there, so a restart inserts the same tag and pushes the same bytes.
func startLiveReload(op, processDir string) (*devLiveReload, error) {
	portFile := filepath.Join(processDir, "livereload.port")
	ln, err := listenKept(portFile)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "listen for the live reload stream")
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(portFile, []byte(strconv.Itoa(port)+"\n"), 0o600); err != nil {
		return nil, errors.Join(fault.Wrapf(err, fault.Internal, op, "keep the live reload port"), ln.Close())
	}
	l := &devLiveReload{port: port, subs: map[chan struct{}]struct{}{}, sites: map[v1.ObjectName]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", l.events)
	l.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = l.srv.Serve(ln) }()
	return l, nil
}

// listenKept listens on the loopback port portFile holds, else on a free one.
func listenKept(portFile string) (net.Listener, error) {
	if b, err := os.ReadFile(portFile); err == nil { //nolint:gosec // dev's own state file
		if port, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && port > 0 {
			if ln, lerr := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); lerr == nil {
				return ln, nil
			}
		}
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

// events streams one `data: reload` message to its client per Site change, until the client or dev goes.
func (l *devLiveReload) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	ch := make(chan struct{}, 1)
	l.mu.Lock()
	l.subs[ch] = struct{}{}
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.subs, ch)
		l.mu.Unlock()
	}()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if _, err := io.WriteString(w, "data: reload\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// url is the stream's address, which the tag opens.
func (l *devLiveReload) url() string {
	return fmt.Sprintf("http://127.0.0.1:%d/events", l.port)
}

// tag is the script dev inserts into a Site's index: it opens the stream and reloads the page on an event.
func (l *devLiveReload) tag() string {
	return `<script>new EventSource("` + l.url() + `").onmessage=function(){location.reload()}</script>`
}

// started records app's Site images as those the open pages show.
func (l *devLiveReload) started(app *v1.App) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sites = siteImages(app)
}

// current notes that app, a revision dev applied, is current: when its Site images differ from those at the last event,
// or when the stream started, open pages reload. It compares with the last event, not the last revision, so a follow a
// newer revision cancelled loses no event.
func (l *devLiveReload) current(app *v1.App) {
	sites := siteImages(app)
	l.mu.Lock()
	defer l.mu.Unlock()
	if maps.Equal(sites, l.sites) {
		return
	}
	l.sites = sites
	for ch := range l.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// stop closes the stream's listener and its clients.
func (l *devLiveReload) stop() {
	_ = l.srv.Close()
}

// siteImages is each Site part's image.
func siteImages(app *v1.App) map[v1.ObjectName]string {
	out := map[v1.ObjectName]string{}
	for _, si := range app.Spec.Sites {
		out[si.Name] = si.Image
	}
	return out
}

// siteCopy copies the Site build at dir into a new folder under stage, refusing what packDir refuses, and inserts the
// tag into its root index file; the caller removes the copy after the push. packDir writes every mode and time as
// constants, so the tag is the copy's only difference in the pushed bytes.
func (l *devLiveReload) siteCopy(op, stage, key, dir, index string) (string, error) {
	dst, err := os.MkdirTemp(stage, "site-"+key+"-")
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "create a copy of images.%s", key)
	}
	if err := copySite(dir, dst); err != nil {
		return "", errors.Join(fault.Invalidf(op, "copy %s: %v", dir, err), os.RemoveAll(dst))
	}
	p := filepath.Join(dst, filepath.FromSlash(index))
	doc, err := os.ReadFile(p) //nolint:gosec // the copy dev just made
	if errors.Is(err, fs.ErrNotExist) {
		return dst, nil
	}
	if err != nil {
		return "", errors.Join(fault.Wrapf(err, fault.Internal, op, "read %s", p), os.RemoveAll(dst))
	}
	if err := os.WriteFile(p, withTag(doc, []byte(l.tag())), 0o600); err != nil {
		return "", errors.Join(fault.Wrapf(err, fault.Internal, op, "write %s", p), os.RemoveAll(dst))
	}
	return dst, nil
}

// copySite copies every entry of src into dst, dot-entries included; an entry that is neither a directory nor a regular
// file is refused, as packDir refuses it. A symlinked src is resolved first, as packDir resolves its root.
func copySite(src, dst string) error {
	root, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case fi.IsDir():
			return os.MkdirAll(target, 0o700)
		case fi.Mode().IsRegular():
			data, err := os.ReadFile(p) //nolint:gosec // a file of the Site build the caller names
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0o600)
		default:
			return fmt.Errorf("%s is not a regular file or directory (symlinks are not packed)", filepath.ToSlash(rel))
		}
	})
}

// withTag inserts tag before the first </head> of doc, found with the x/net/html tokenizer as ADR-0239's index copy
// finds its place, so a </head> inside a comment or a script is not one; without one it appends tag.
func withTag(doc, tag []byte) []byte {
	at := len(doc)
	z := html.NewTokenizer(bytes.NewReader(doc))
	for pos := 0; ; {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		start := pos
		pos += len(z.Raw())
		if tt != html.EndTagToken {
			continue
		}
		if name, _ := z.TagName(); string(name) == "head" {
			at = start
			break
		}
	}
	out := make([]byte, 0, len(doc)+len(tag))
	out = append(out, doc[:at]...)
	out = append(out, tag...)
	return append(out, doc[at:]...)
}
