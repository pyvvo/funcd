package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"syscall"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// maxTokenFileBytes bounds a token file read (ADR-0171 Decision 2).
const maxTokenFileBytes = 4096

// credentialOption maps auth.* to the credential option (ADR-0171): auth.credentials ⇒ WithCredentials, else the
// auth.token shorthand ⇒ WithDevAuth. It reads every token file and checks every entry, so buildOptions calls it
// before it opens Badger, the substrate or KV. Errors name keys, paths and indexes, never a token.
func credentialOption(cfg config.Config, log *slog.Logger) (funcd.Option, error) {
	const op = "credentialOption"
	if cfg.Auth.Credentials == nil {
		token := cfg.Auth.Token
		if token == "" {
			token = funcd.DevToken
			log.Warn("funcd: no auth.token / FUNCD_TOKEN — using the built-in dev token (not for production)")
		}
		return funcd.WithDevAuth(token, cfg.Auth.Namespaces...), nil
	}
	creds := make([]funcd.Credential, 0, len(cfg.Auth.Credentials))
	first := make(map[string]int, len(cfg.Auth.Credentials))
	perRole := map[string]int{}
	for i, e := range cfg.Auth.Credentials {
		key := fmt.Sprintf("auth.credentials[%d]", i)
		nss := e.Namespaces
		switch {
		case nss != nil && len(nss) == 0:
			return nil, fault.Invalidf(op, "config key %q is empty: omit it for default", key+".namespaces")
		case e.Role == "admin" && nss != nil:
			return nil, fault.Invalidf(op, "config key %q is set on an admin, which spans every namespace: remove it", key+".namespaces")
		case e.Role != "admin" && nss == nil:
			nss = []string{"default"}
		}
		token, err := readTokenFile(key+".tokenFile", e.TokenFile)
		if err != nil {
			return nil, err
		}
		if token == funcd.DevToken {
			return nil, fault.Invalidf(op, "config key %q: token file %q holds the built-in dev token: generate a new one", key+".tokenFile", e.TokenFile)
		}
		if j, dup := first[token]; dup {
			return nil, fault.Invalidf(op, "config keys %q and %q hold the same token", fmt.Sprintf("auth.credentials[%d]", j), key)
		}
		first[token] = i
		perRole[e.Role]++
		creds = append(creds, funcd.Credential{Token: token, Role: e.Role, Namespaces: nss})
	}
	log.Info("funcd: static credentials loaded",
		"admin", perRole["admin"], "developer", perRole["developer"], "viewer", perRole["viewer"])
	return funcd.WithCredentials(creds...), nil
}

// readTokenFile opens path read-only and non-blocking (a FIFO never stalls startup), checks the opened file, and
// reads its one token (ADR-0171 Decision 2).
func readTokenFile(key, path string) (string, error) {
	const op = "readTokenFile"
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // operator-supplied token file
	if err != nil {
		return "", fault.Invalidf(op, "config key %q: open token file: %v", key, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", fault.Invalidf(op, "config key %q: stat token file: %v", key, err)
	}
	if err := checkTokenFile(fi, os.Geteuid()); err != nil {
		return "", fault.Invalidf(op, "config key %q: token file %q %v", key, path, err)
	}
	token, err := readToken(f)
	if err != nil {
		return "", fault.Invalidf(op, "config key %q: token file %q %v", key, path, err)
	}
	return token, nil
}

// readToken reads at most maxTokenFileBytes and returns the one token, ASCII whitespace trimmed.
func readToken(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxTokenFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("could not be read: %w", err)
	}
	if len(b) > maxTokenFileBytes {
		return "", fmt.Errorf("is larger than %d bytes", maxTokenFileBytes)
	}
	token := strings.Trim(string(b), " \t\n\v\f\r")
	if token == "" {
		return "", errors.New("holds no token (empty or whitespace only)")
	}
	for i := range len(token) {
		if c := token[i]; c < 0x21 || c > 0x7e {
			return "", errors.New("must hold one token of printable ASCII (0x21-0x7E) on one line")
		}
	}
	return token, nil
}

// checkTokenFile refuses a token file that is not regular, that a group or other can access, or that neither the
// daemon's effective UID nor root owns.
func checkTokenFile(fi os.FileInfo, euid int) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("is not a regular file (%s)", fi.Mode().Type())
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("has mode %#o, open to group or others: chmod 0600 (or 0400); "+
			"a Kubernetes secret volume needs defaultMode: 0400, a Docker secret mode=0400 with uid=%d", perm, euid)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("has no owner information")
	}
	if uid := int(st.Uid); uid != euid && uid != 0 {
		return fmt.Errorf("is owned by uid %d, not the daemon's uid %d or root", uid, euid)
	}
	return nil
}
