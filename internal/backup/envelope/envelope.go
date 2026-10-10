// Package envelope seals a backup generation to operator-held age recipients and names the keys it needs
// (ADR-0204 Decisions 1-3, 5): the box writes what it cannot read.
package envelope

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"

	"filippo.io/age"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup"
)

// fingerprintLabel keeps Fingerprint apart from SHA-256(master), the catalog-token HMAC key (Decision 3).
const fingerprintLabel = "funcd-key-fingerprint-v1"

// ageIntro opens every age file; Opener refuses a store file whose seal disagrees with its manifest.
const ageIntro = "age-encryption.org/v1\n"

// Config is backup.encryption (Decision 2). Recipients are the paths of age recipients files; SecretsKey is
// secrets.encryptionKeyFile's bytes (nil ⇒ none); Master is the node master secret. NoSecrets (ADR-0209), for a store
// holding no Secret, skips only the secrets-key rule and the secrets-key warning. KeyPrefix names the config keys in
// errors ("" ⇒ "backup.", as backup.Config); Logger takes the warnings and the start line (nil ⇒ slog.Default()).
type Config struct {
	Recipients         []string
	None, NoSecrets    bool
	SecretsKey, Master []byte
	KeyPrefix          string
	Logger             *slog.Logger
}

// Sealer seals the store files of a run to the configured recipients and carries the manifest's key records.
type Sealer struct {
	recipients []age.Recipient
	keys       backup.Keys
}

// New checks cfg against Decision 2; a violation is fault.Invalid naming the key and file. It logs the keys'
// fingerprints, never their bytes, and the warnings of an unsealed or secrets-key-less configuration.
func New(cfg Config) (*Sealer, error) {
	const op = "envelope.New"
	prefix := cfg.KeyPrefix
	if prefix == "" {
		prefix = "backup."
	}
	recipientsKey, noneKey := prefix+"encryption.recipients", prefix+"encryption.none"
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Sealer{}
	if cfg.SecretsKey != nil {
		s.keys.SecretsKey = Fingerprint(cfg.SecretsKey)
	}
	if cfg.Master != nil {
		s.keys.MasterSecret = Fingerprint(cfg.Master)
	}
	if cfg.None {
		switch {
		case len(cfg.Recipients) > 0:
			return nil, fault.Invalidf(op, "%s is true: %s must be empty", noneKey, recipientsKey)
		case cfg.SecretsKey == nil && !cfg.NoSecrets:
			return nil, fault.Invalidf(op, "%s is true and secrets.encryptionKeyFile is not set: a backup would carry "+
				"Secret values in plaintext; set secrets.encryptionKeyFile or %s", noneKey, recipientsKey)
		}
		what := "every record but Secret values leaves in plaintext"
		if cfg.NoSecrets {
			what = "every record leaves in plaintext"
		}
		log.Warn("backup encryption is off: "+what, "key", noneKey)
		s.logKeys(log)
		return s, nil
	}
	rs, err := readRecipients(op, recipientsKey, cfg.Recipients)
	if err != nil {
		return nil, err
	}
	s.recipients = rs
	for _, r := range rs {
		s.keys.Recipients = append(s.keys.Recipients, Fingerprint([]byte(recipientString(r))))
	}
	slices.Sort(s.keys.Recipients)
	if cfg.SecretsKey == nil && !cfg.NoSecrets {
		log.Warn("no secrets.encryptionKeyFile: the backup envelope alone protects Secret values", "key", recipientsKey)
	}
	s.logKeys(log)
	return s, nil
}

// readRecipients parses every file, keeps X25519 and hybrid recipients, de-duplicates them by their String(), and
// requires at least two that seal together (age refuses hybrid beside X25519).
func readRecipients(op, key string, paths []string) ([]age.Recipient, error) {
	if len(paths) == 0 {
		return nil, fault.Invalidf(op, "%s is empty: set at least 2 age recipients, or the encryption none key", key)
	}
	seen := map[string]bool{}
	var out []age.Recipient
	for _, p := range paths {
		f, err := os.Open(p) //nolint:gosec // an operator-named recipients file
		if err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "%s: open %s", key, p)
		}
		rs, err := age.ParseRecipients(f)
		_ = f.Close()
		if err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "%s: parse %s (X25519 age1… or hybrid age1pq1… only)", key, p)
		}
		for _, r := range rs {
			str := recipientString(r)
			if str == "" {
				return nil, fault.Invalidf(op, "%s: %s holds a recipient that is not X25519 or hybrid", key, p)
			}
			if !seen[str] {
				seen[str] = true
				out = append(out, r)
			}
		}
	}
	if len(out) < 2 {
		return nil, fault.Invalidf(op, "%s (%s): %d distinct recipient(s), want at least 2 (an operator and a recovery one)",
			key, strings.Join(paths, ", "), len(out))
	}
	if _, err := age.Encrypt(io.Discard, out...); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "%s (%s): the recipients do not seal together; a hybrid "+
			"(age1pq1…) recipient cannot be mixed with an X25519 (age1…) one", key, strings.Join(paths, ", "))
	}
	return out, nil
}

// recipientString is the canonical String() of an X25519 or hybrid recipient, "" for any other type.
func recipientString(r age.Recipient) string {
	switch r := r.(type) {
	case *age.X25519Recipient:
		return r.String()
	case *age.HybridRecipient:
		return r.String()
	}
	return ""
}

func (s *Sealer) logKeys(log *slog.Logger) {
	log.Info("backup keys", "secretsKey", s.keys.SecretsKey, "masterSecret", s.keys.MasterSecret,
		"recipients", s.keys.Recipients)
}

// Seal is one age stream per store file to every recipient; nil when none.
func (s *Sealer) Seal() backup.Seal {
	if len(s.recipients) == 0 {
		return nil
	}
	rs := s.recipients
	return func(dst io.Writer) (io.WriteCloser, error) { return age.Encrypt(dst, rs...) }
}

// Keys are the manifest's key records (Decision 3): Recipients sorted and distinct, nil when none.
func (s *Sealer) Keys() backup.Keys {
	k := s.keys
	k.Recipients = slices.Clone(k.Recipients)
	return k
}

// Fingerprint is the first 16 lowercase hex of SHA-256(label ‖ 0x00 ‖ b) (Decision 3).
func Fingerprint(b []byte) string {
	h := sha256.New()
	h.Write([]byte(fingerprintLabel))
	h.Write([]byte{0})
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ReadIdentities reads the operator's age identity files (Decision 5): X25519 or hybrid identities only; another
// type, or a file that does not parse (one encrypted with age -p), is fault.Invalid naming the file.
func ReadIdentities(paths []string) ([]age.Identity, error) {
	const op = "envelope.ReadIdentities"
	var out []age.Identity
	for _, p := range paths {
		f, err := os.Open(p) //nolint:gosec // an operator-named identity file
		if err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "open identity file %s", p)
		}
		ids, err := age.ParseIdentities(f)
		_ = f.Close()
		if err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "parse identity file %s (X25519 or hybrid, not passphrase-encrypted)", p)
		}
		for _, id := range ids {
			if identityFingerprint(id) == "" {
				return nil, fault.Invalidf(op, "identity file %s holds an identity that is not X25519 or hybrid", p)
			}
			out = append(out, id)
		}
	}
	return out, nil
}

// identityFingerprint fingerprints an identity's recipient as Keys records it; "" for another type.
func identityFingerprint(id age.Identity) string {
	switch id := id.(type) {
	case *age.X25519Identity:
		return Fingerprint([]byte(id.Recipient().String()))
	case *age.HybridIdentity:
		return Fingerprint([]byte(id.Recipient().String()))
	}
	return ""
}

// Opener binds the operator's identities (Decision 5). For a manifest naming recipients, the Unseal decrypts with the
// identities among them, and no match is fault.Invalid naming both fingerprint sets. A manifest naming none reads
// the bytes as is. Either way a store file whose seal disagrees with its manifest is fault.Invalid.
func Opener(ids []age.Identity) backup.Opener {
	byFP := map[string]age.Identity{}
	var have []string
	for _, id := range ids {
		if fp := identityFingerprint(id); fp != "" {
			byFP[fp] = id
			have = append(have, fp)
		}
	}
	slices.Sort(have)
	return func(recipients []string) (backup.Unseal, error) {
		const op = "envelope.Opener"
		if len(recipients) == 0 {
			return unsealed, nil
		}
		var match []age.Identity
		for _, fp := range recipients {
			if id, ok := byFP[fp]; ok {
				match = append(match, id)
			}
		}
		if len(match) == 0 {
			return nil, fault.Invalidf(op, "no identity opens this generation: it is sealed to recipients %v, the "+
				"identities given are %v", recipients, have)
		}
		return func(src io.Reader) (io.Reader, error) {
			br, sealed, err := peekSealed(src)
			if err != nil {
				return nil, err
			}
			if !sealed {
				return nil, fault.Invalidf(op, "the manifest names recipients %v but the store file is not sealed", recipients)
			}
			r, err := age.Decrypt(br, match...)
			if err != nil {
				return nil, fault.Wrapf(err, fault.Invalid, op, "open the sealed store file")
			}
			return r, nil
		}, nil
	}
}

// unsealed reads a store file of a manifest naming no recipient as is, refusing one that is sealed.
func unsealed(src io.Reader) (io.Reader, error) {
	br, sealed, err := peekSealed(src)
	if err != nil {
		return nil, err
	}
	if sealed {
		return nil, fault.Invalidf("envelope.Opener", "the manifest names no recipient but the store file is sealed")
	}
	return br, nil
}

// peekSealed reports whether src begins with the age header, without consuming it.
func peekSealed(src io.Reader) (*bufio.Reader, bool, error) {
	br := bufio.NewReader(src)
	head, err := br.Peek(len(ageIntro))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, fault.Wrapf(err, fault.Internal, "envelope.Opener", "read the store file")
	}
	return br, bytes.Equal(head, []byte(ageIntro)), nil
}
