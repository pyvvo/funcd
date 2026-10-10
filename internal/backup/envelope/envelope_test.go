package envelope_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/escrow"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/secrets/aesgcm"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const (
	recipientsKey = "backup.encryption.recipients"
	timeline      = "1111111111111111"
)

// source is a store of fixed records at a version.
type source struct {
	version string
	records []snapshot.Record
}

func (s source) Snapshot(_ context.Context, emit func(snapshot.Record) error) (string, error) {
	for _, r := range s.records {
		if err := emit(r); err != nil {
			return "", err
		}
	}
	return s.version, nil
}

func rec(k, v string) snapshot.Record { return snapshot.Record{Key: []byte(k), Value: []byte(v)} }

func randomKey(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func x25519(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	return id
}

func hybrid(t *testing.T) *age.HybridIdentity {
	t.Helper()
	id, err := age.GenerateHybridIdentity()
	require.NoError(t, err)
	return id
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "keys.txt")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

// recipientsFile writes an age recipients file, one recipient per line under a comment.
func recipientsFile(t *testing.T, rs ...string) string {
	t.Helper()
	return writeFile(t, "# backup recipients\n"+strings.Join(rs, "\n")+"\n")
}

func fp(s string) string { return envelope.Fingerprint([]byte(s)) }

func newSealer(t *testing.T, cfg envelope.Config) (*envelope.Sealer, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
	s, err := envelope.New(cfg)
	require.NoError(t, err)
	return s, logs
}

func openTarget(t *testing.T, dir string) backup.Target {
	t.Helper()
	tg, err := backup.Open(context.Background(), backup.Config{
		Target: gocloud.FileURL(dir), DataDir: t.TempDir(), Retention: backup.Retention{Hourly: 48},
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = tg.Close() })
	return tg
}

// storeFile concatenates a generation's parts of one store, as `cat part-*` does.
func storeFile(t *testing.T, dir string, m backup.Manifest, name string) []byte {
	t.Helper()
	gen, err := filepath.Glob(filepath.Join(dir, "gen", "*", fmt.Sprintf("%010d-%s", m.Generation, m.Timeline), name, "part-*"))
	require.NoError(t, err)
	require.NotEmpty(t, gen)
	slices.Sort(gen)
	var out []byte
	for _, p := range gen {
		b, err := os.ReadFile(p) //nolint:gosec // a test temp path
		require.NoError(t, err)
		out = append(out, b...)
	}
	return out
}

func readAll(t *testing.T, r io.Reader) []snapshot.Record {
	t.Helper()
	next := backup.Records(r)
	var out []snapshot.Record
	for {
		r, err := next()
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err)
		out = append(out, r)
	}
}

// scenario: sealed-to-recipients — each store file's parts, concatenated, open with the age CLI and either identity
// alone, and not with a third.
func TestScenarioSealedToRecipients(t *testing.T) {
	t.Parallel()
	a, b, c := x25519(t), x25519(t), x25519(t)
	s, _ := newSealer(t, envelope.Config{
		Recipients: []string{recipientsFile(t, a.Recipient().String()), recipientsFile(t, b.Recipient().String())},
		SecretsKey: randomKey(t), Master: randomKey(t),
	})
	dir := t.TempDir()
	big := rec("m2", strings.Repeat("x", 9<<20))
	want := map[string][]snapshot.Record{
		"events":    {rec("e1", "event")},
		"metastore": {rec("m1", "config"), big},
		"runs":      {rec("r1", "run")},
	}
	m, err := openTarget(t, dir).Write(context.Background(), source{records: want["events"]},
		source{version: timeline + "-7", records: want["metastore"]}, source{records: want["runs"]},
		backup.WriteOptions{Seal: s.Seal(), Keys: s.Keys()})
	require.NoError(t, err)

	agePath, _ := exec.LookPath("age")
	for _, f := range m.Stores {
		sealed := storeFile(t, dir, m, f.Name)
		if f.Name == "metastore" {
			require.Equal(t, 2, f.Parts, "a sealed file over 8 MiB is cut into parts")
		}
		sum := sha256.Sum256(sealed)
		require.Equal(t, hex.EncodeToString(sum[:]), f.SHA256, "the checksum covers the sealed bytes")
		require.Equal(t, int64(len(sealed)), f.Bytes)
		require.False(t, bytes.Contains(sealed, []byte("config")), "%s is sealed", f.Name)
		for _, id := range []age.Identity{a, b} {
			r, err := age.Decrypt(bytes.NewReader(sealed), id)
			require.NoError(t, err)
			require.Equal(t, want[f.Name], readAll(t, r))
		}
		_, err := age.Decrypt(bytes.NewReader(sealed), c)
		var noMatch *age.NoIdentityMatchError
		require.ErrorAs(t, err, &noMatch, "a third identity does not open %s", f.Name)

		if agePath != "" {
			in := filepath.Join(t.TempDir(), f.Name+".age")
			require.NoError(t, os.WriteFile(in, sealed, 0o600))
			idFile := writeFile(t, a.String()+"\n")
			out, err := exec.CommandContext(context.Background(), agePath, "-d", "-i", idFile, in).Output() //nolint:gosec // the age CLI on PATH
			require.NoError(t, err)
			require.Equal(t, want[f.Name], readAll(t, bytes.NewReader(out)))
		}
	}
}

// scenario: recipients-required — no recipient, one, one in two files, or an X25519 and a hybrid one is fault.Invalid
// naming backup.encryption.recipients.
func TestScenarioRecipientsRequired(t *testing.T) {
	t.Parallel()
	a, h := x25519(t).Recipient().String(), hybrid(t).Recipient().String()
	for name, files := range map[string][]string{
		"none":              nil,
		"one":               {recipientsFile(t, a)},
		"one in two files":  {recipientsFile(t, a), recipientsFile(t, a)},
		"x25519 and hybrid": {recipientsFile(t, a), recipientsFile(t, h)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := envelope.New(envelope.Config{Recipients: files, SecretsKey: []byte("k"), Logger: slog.New(slog.DiscardHandler)})
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			require.ErrorContains(t, err, recipientsKey)
		})
	}
}

// scenario: plaintext-secrets-refused — none without secrets.encryptionKeyFile refuses naming both keys; with it the
// sealer starts, warns, and the generation holds no plaintext Secret.
func TestScenarioPlaintextSecretsRefused(t *testing.T) {
	t.Parallel()
	_, err := envelope.New(envelope.Config{None: true, Logger: slog.New(slog.DiscardHandler)})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "backup.encryption.none")
	require.ErrorContains(t, err, "secrets.encryptionKeyFile")

	key := randomKey(t)
	s, logs := newSealer(t, envelope.Config{None: true, SecretsKey: key})
	require.Nil(t, s.Seal())
	require.Contains(t, logs.String(), "every record but Secret values leaves in plaintext")

	enc, err := aesgcm.NewAESEncryptor(key)
	require.NoError(t, err)
	ctx := context.Background()
	meta := store.New(memory.New(), store.WithEncryptor([]v1.Kind{v1.KindSecret}, enc))
	obj, _ := v1.NewObject(v1.KindSecret)
	sec, _ := obj.(*v1.Secret)
	sec.Name, sec.Namespace, sec.ResourceGroup = "db-pw", "default", "rg1"
	value := "plaintext-secret-value"
	sec.Spec.Data = map[string][]byte{"password": []byte(value)}
	_, err = meta.Create(ctx, sec)
	require.NoError(t, err)

	dir := t.TempDir()
	m, err := openTarget(t, dir).Write(ctx, source{}, meta, source{}, backup.WriteOptions{Seal: s.Seal(), Keys: s.Keys()})
	require.NoError(t, err)
	file := storeFile(t, dir, m, "metastore")
	require.Contains(t, string(file), "db-pw", "the unsealed metastore file holds the records as stored")
	require.NotContains(t, string(file), value)
	require.NotContains(t, string(file), base64.StdEncoding.EncodeToString([]byte(value)))
}

// scenario: manifest-names-keys — the manifest records the fingerprints of K, M and both recipients, and no object
// (nor the start log) holds K, M or SHA-256(M).
func TestScenarioManifestNamesKeys(t *testing.T) {
	t.Parallel()
	k, mk := randomKey(t), randomKey(t)
	a, b := x25519(t).Recipient().String(), x25519(t).Recipient().String()
	s, logs := newSealer(t, envelope.Config{Recipients: []string{recipientsFile(t, a, b)}, SecretsKey: k, Master: mk})
	dir := t.TempDir()
	m, err := openTarget(t, dir).Write(context.Background(), source{records: []snapshot.Record{rec("e", "v")}},
		source{version: timeline + "-7", records: []snapshot.Record{rec("m", "v")}}, source{}, backup.WriteOptions{Seal: s.Seal(), Keys: s.Keys()})
	require.NoError(t, err)

	wantRecipients := []string{fp(a), fp(b)}
	slices.Sort(wantRecipients)
	manifests, err := filepath.Glob(filepath.Join(dir, "gen", "*", "*", "manifest.yaml"))
	require.NoError(t, err)
	require.Len(t, manifests, 1)
	raw, err := os.ReadFile(manifests[0])
	require.NoError(t, err)
	require.Contains(t, string(raw), "secretsKey: "+envelope.Fingerprint(k))
	require.Contains(t, string(raw), "masterSecret: "+envelope.Fingerprint(mk))
	require.Equal(t, backup.Keys{SecretsKey: envelope.Fingerprint(k), MasterSecret: envelope.Fingerprint(mk),
		Recipients: wantRecipients}, m.Keys)

	hm := sha256.Sum256(mk)
	secrets := [][]byte{k, mk, hm[:]}
	for _, x := range [][]byte{k, mk, hm[:]} {
		secrets = append(secrets, []byte(hex.EncodeToString(x)))
	}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p) //nolint:gosec // a test temp path
		require.NoError(t, err)
		for _, x := range secrets {
			require.False(t, bytes.Contains(b, x), "%s holds key material", p)
		}
		return nil
	}))
	for _, x := range secrets {
		require.False(t, bytes.Contains(logs.Bytes(), x), "the start log holds key material")
	}
	require.Contains(t, logs.String(), envelope.Fingerprint(mk), "the start log names the fingerprints")
}

// scenario: recipients-change — generations 1 and 2 sealed to A and B, 3 to A and C: 3 records A and C, B opens 1 and
// 2, and opening 3 with B fails naming both sets.
func TestScenarioRecipientsChange(t *testing.T) {
	t.Parallel()
	a, b, c := x25519(t), x25519(t), x25519(t)
	ab, _ := newSealer(t, envelope.Config{Recipients: []string{recipientsFile(t, a.Recipient().String(), b.Recipient().String())}, SecretsKey: []byte("k")})
	ac, _ := newSealer(t, envelope.Config{Recipients: []string{recipientsFile(t, a.Recipient().String(), c.Recipient().String())}, SecretsKey: []byte("k")})
	dir := t.TempDir()
	tg := openTarget(t, dir)
	var ms []backup.Manifest
	for _, s := range []*envelope.Sealer{ab, ab, ac} {
		m, err := tg.Write(context.Background(), source{}, source{version: timeline + "-7", records: []snapshot.Record{rec("m", "v")}},
			source{}, backup.WriteOptions{Seal: s.Seal(), Keys: s.Keys()})
		require.NoError(t, err)
		ms = append(ms, m)
	}
	acFPs := []string{fp(a.Recipient().String()), fp(c.Recipient().String())}
	slices.Sort(acFPs)
	require.Equal(t, acFPs, ms[2].Recipients)

	open := envelope.Opener([]age.Identity{b})
	for _, m := range ms[:2] {
		unseal, err := open(m.Recipients)
		require.NoError(t, err)
		r, err := unseal(bytes.NewReader(storeFile(t, dir, m, "metastore")))
		require.NoError(t, err)
		require.Equal(t, []snapshot.Record{rec("m", "v")}, readAll(t, r))
	}
	_, err := open(ms[2].Recipients)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	for _, f := range append(acFPs, fp(b.Recipient().String())) {
		require.ErrorContains(t, err, f)
	}
}

// The fingerprint is labelled: neither a SHA-256 prefix of the bytes nor of the catalog-token HMAC key.
func TestFingerprintIsLabelled(t *testing.T) {
	b := randomKey(t)
	got := envelope.Fingerprint(b)
	require.Regexp(t, "^[0-9a-f]{16}$", got)
	sum := sha256.Sum256(b)
	require.NotEqual(t, hex.EncodeToString(sum[:])[:16], got)
	hs := sha256.Sum256(sum[:])
	require.NotEqual(t, hex.EncodeToString(hs[:])[:16], got)
	label := sha256.Sum256(append([]byte("funcd-key-fingerprint-v1\x00"), b...))
	require.Equal(t, hex.EncodeToString(label[:])[:16], got)
}

// One row per Decision 2 rule.
func TestNewRules(t *testing.T) {
	a, b := x25519(t).Recipient().String(), x25519(t).Recipient().String()
	pair := recipientsFile(t, a, b)
	for _, tc := range []struct {
		name    string
		cfg     envelope.Config
		errHas  []string
		warnHas string
	}{
		{name: "none with recipients", cfg: envelope.Config{None: true, Recipients: []string{pair}, SecretsKey: []byte("k")},
			errHas: []string{"backup.encryption.none", recipientsKey}},
		{name: "none without a secrets key", cfg: envelope.Config{None: true}, errHas: []string{"backup.encryption.none", "secrets.encryptionKeyFile"}},
		{name: "none under NoSecrets", cfg: envelope.Config{None: true, NoSecrets: true}, warnHas: "every record leaves in plaintext"},
		{name: "none with a secrets key", cfg: envelope.Config{None: true, SecretsKey: []byte("k")}, warnHas: "every record but Secret values leaves in plaintext"},
		{name: "recipients without a secrets key", cfg: envelope.Config{Recipients: []string{pair}}, warnHas: "envelope alone protects Secret values"},
		{name: "recipients under NoSecrets", cfg: envelope.Config{Recipients: []string{pair}, NoSecrets: true}},
		{name: "a missing file", cfg: envelope.Config{Recipients: []string{filepath.Join(t.TempDir(), "absent")}}, errHas: []string{recipientsKey, "absent"}},
		{name: "the KV prefix", cfg: envelope.Config{KeyPrefix: "kvstore.backup."}, errHas: []string{"kvstore.backup.encryption.recipients"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			tc.cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
			_, err := envelope.New(tc.cfg)
			if tc.errHas != nil {
				require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
				for _, s := range tc.errHas {
					require.ErrorContains(t, err, s)
				}
				return
			}
			require.NoError(t, err)
			if tc.warnHas != "" {
				require.Contains(t, logs.String(), "level=WARN")
				require.Contains(t, logs.String(), tc.warnHas)
			} else {
				require.NotContains(t, logs.String(), "level=WARN")
			}
		})
	}
}

// No SSH and no plugin recipient parses.
func TestRecipientsRefuseSSHAndPlugin(t *testing.T) {
	a := x25519(t).Recipient().String()
	for name, line := range map[string]string{
		"ssh":    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHsKLqeplhpW+uObz5dvMgjz1OxfM/XXUB+VHtZ6isGN",
		"plugin": "age1yubikey1qwt50d05nh5vutpdzmlg5wn80xq5negm4uj9ghv0snvdd3yysf5yw3rhl3t",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := envelope.New(envelope.Config{Recipients: []string{recipientsFile(t, a, line)}, Logger: slog.New(slog.DiscardHandler)})
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.ErrorContains(t, err, recipientsKey)
		})
	}
}

// Keys lists each distinct recipient once, sorted, whatever the files' order and repeats.
func TestKeysSortedAndDistinct(t *testing.T) {
	a, b, c := x25519(t).Recipient().String(), x25519(t).Recipient().String(), x25519(t).Recipient().String()
	s, _ := newSealer(t, envelope.Config{Recipients: []string{recipientsFile(t, c, a), recipientsFile(t, b, a, c)}})
	want := []string{fp(a), fp(b), fp(c)}
	slices.Sort(want)
	require.Equal(t, want, s.Keys().Recipients)
	k := s.Keys()
	k.Recipients[0] = "changed"
	require.Equal(t, want, s.Keys().Recipients, "Keys returns a copy")
}

// none records both fingerprints and no recipient, and CheckSecretsKey passes with that key.
func TestKeysWhenNone(t *testing.T) {
	k, mk := randomKey(t), randomKey(t)
	s, _ := newSealer(t, envelope.Config{None: true, SecretsKey: k, Master: mk})
	keys := s.Keys()
	require.Equal(t, backup.Keys{SecretsKey: envelope.Fingerprint(k), MasterSecret: envelope.Fingerprint(mk)}, keys)
	require.Nil(t, keys.Recipients)
	require.NoError(t, escrow.CheckSecretsKey(backup.Manifest{Keys: keys}, k, ""))
}

// A manifest naming no recipient reads its files as is; a seal that disagrees with the manifest is refused.
func TestOpenerUnsealed(t *testing.T) {
	a := x25519(t)
	unseal, err := envelope.Opener(nil)(nil)
	require.NoError(t, err)
	r, err := unseal(strings.NewReader("plain bytes"))
	require.NoError(t, err)
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "plain bytes", string(got))
	r, err = unseal(strings.NewReader(""))
	require.NoError(t, err)
	got, err = io.ReadAll(r)
	require.NoError(t, err)
	require.Empty(t, got)

	var sealed bytes.Buffer
	w, err := age.Encrypt(&sealed, a.Recipient())
	require.NoError(t, err)
	_, _ = w.Write([]byte("records"))
	require.NoError(t, w.Close())
	_, err = unseal(bytes.NewReader(sealed.Bytes()))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a sealed file under a manifest naming no recipient")

	open, err := envelope.Opener([]age.Identity{a})([]string{fp(a.Recipient().String())})
	require.NoError(t, err)
	_, err = open(strings.NewReader("plain bytes"))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an unsealed file under a manifest naming recipients")
	r, err = open(bytes.NewReader(sealed.Bytes()))
	require.NoError(t, err)
	got, err = io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "records", string(got))
}

// An X25519 and a hybrid identity each match the fingerprint Keys records for its recipient.
func TestIdentityMatchesRecipientFingerprint(t *testing.T) {
	for name, pair := range map[string][2]age.Identity{
		"x25519": {x25519(t), x25519(t)},
		"hybrid": {hybrid(t), hybrid(t)},
	} {
		t.Run(name, func(t *testing.T) {
			var lines, secrets []string
			for _, id := range pair {
				switch id := id.(type) {
				case *age.X25519Identity:
					lines, secrets = append(lines, id.Recipient().String()), append(secrets, id.String())
				case *age.HybridIdentity:
					lines, secrets = append(lines, id.Recipient().String()), append(secrets, id.String())
				}
			}
			s, _ := newSealer(t, envelope.Config{Recipients: []string{recipientsFile(t, lines...)}, SecretsKey: []byte("k")})
			ids, err := envelope.ReadIdentities([]string{writeFile(t, "# operator\n"+secrets[1]+"\n")})
			require.NoError(t, err)
			unseal, err := envelope.Opener(ids)(s.Keys().Recipients)
			require.NoError(t, err)
			var buf bytes.Buffer
			w, err := s.Seal()(&buf)
			require.NoError(t, err)
			_, _ = w.Write([]byte("hello"))
			require.NoError(t, w.Close())
			r, err := unseal(&buf)
			require.NoError(t, err)
			got, err := io.ReadAll(r)
			require.NoError(t, err)
			require.Equal(t, "hello", string(got))
		})
	}
}

// A file that is not X25519 or hybrid identities — recipients, an SSH key, a passphrase-encrypted file — is
// fault.Invalid naming the file.
func TestReadIdentitiesRefusesOtherTypes(t *testing.T) {
	var enc bytes.Buffer
	sr, err := age.NewScryptRecipient("passphrase")
	require.NoError(t, err)
	sr.SetWorkFactor(10)
	w, err := age.Encrypt(&enc, sr)
	require.NoError(t, err)
	_, _ = w.Write([]byte(x25519(t).String() + "\n"))
	require.NoError(t, w.Close())
	for name, content := range map[string]string{
		"recipients": x25519(t).Recipient().String() + "\n",
		"ssh":        "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n",
		"passphrase": enc.String(),
	} {
		t.Run(name, func(t *testing.T) {
			p := writeFile(t, content)
			_, err := envelope.ReadIdentities([]string{p})
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.ErrorContains(t, err, p)
		})
	}
}
