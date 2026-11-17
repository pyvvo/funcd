package s3gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/nsmaster"
)

// Deriver derives and checks in-platform key pairs (ADR-0270). A positive Period gives a pair that holds the time
// window it was derived in and a per-start Salt; a zero Period gives the fixed pair of DeriveKeypair (funcdctl dev).
type Deriver struct {
	Master []byte
	// NamespaceMasters, when set, gives each namespace's own master (FEAT-0002 decision 8, on a worker node); nil
	// derives every namespace's pairs from Master.
	NamespaceMasters nsmaster.Source
	Salt             []byte
	Period           time.Duration
	Now              func() time.Time // nil: time.Now
}

func (d Deriver) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

func (d Deriver) window(t time.Time) int64 { return t.UnixNano() / int64(d.Period) }

// Keypair is the identity's pair for the current window; the zero pair when the namespace has no master here.
func (d Deriver) Keypair(kind v1.Kind, ns, name string) Keypair {
	kp, _ := d.KeypairOf(kind, ns, name)
	return kp
}

// KeypairOf is Keypair, with the error of a namespace this process holds no master for.
func (d Deriver) KeypairOf(kind v1.Kind, ns, name string) (Keypair, error) {
	master, err := d.NamespaceMasters.Of(d.Master, v1.NamespaceName(ns))
	if err != nil {
		return Keypair{}, err
	}
	if d.Period <= 0 {
		return DeriveKeypair(master, kind, ns, name), nil
	}
	w := d.window(d.now())
	access := accessKeyPrefix + accessEncoding.EncodeToString(
		[]byte(kindCode(kind)+refSeparator+ns+refSeparator+name+refSeparator+strconv.FormatInt(w, 10)))
	return Keypair{AccessKey: access, SecretKey: secretOf(master, d.Salt, kind, ns, name, w)}, nil
}

func secretOf(master, salt []byte, kind v1.Kind, ns, name string, w int64) string {
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte("s3:" + string(kind) + ":" + ns + "/" + name + ":" + strconv.FormatInt(w, 10) + ":" + hex.EncodeToString(salt)))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Secret decodes access and derives its secret. With a Period it accepts only a four-part key whose window is the
// current or the previous one; with a zero Period only a three-part key. ok is false otherwise.
func (d Deriver) Secret(access string) (kind v1.Kind, ns, name, secret string, ok bool) {
	kind, ns, name, win, ok := decodeParts(access)
	if !ok {
		return "", "", "", "", false
	}
	master, err := d.NamespaceMasters.Of(d.Master, v1.NamespaceName(ns))
	if err != nil {
		return "", "", "", "", false
	}
	if d.Period <= 0 {
		if win != "" {
			return "", "", "", "", false
		}
		return kind, ns, name, DeriveKeypair(master, kind, ns, name).SecretKey, true
	}
	w, err := strconv.ParseInt(win, 10, 64)
	if err != nil || w < 0 || strconv.FormatInt(w, 10) != win {
		return "", "", "", "", false
	}
	if cur := d.window(d.now()); w != cur && w != cur-1 {
		return "", "", "", "", false
	}
	return kind, ns, name, secretOf(master, d.Salt, kind, ns, name, w), true
}

// RenewalAge is the worker age, between 80% and 90% of Period, at which the identity's worker is replaced; 0 with a
// zero Period (never).
func (d Deriver) RenewalAge(kind v1.Kind, ns, name string) time.Duration {
	if d.Period <= 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(string(kind) + refSeparator + ns + refSeparator + name))
	spread := uint64(d.Period / 10)
	if spread == 0 {
		return d.Period * 8 / 10
	}
	return d.Period*8/10 + time.Duration(binary.BigEndian.Uint64(sum[:8])%spread)
}
