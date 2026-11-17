package multinode

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// tokenPrefix marks a join token in the k3s "K10" form: K10<CA hash>::<node>:<secret> (FEAT-0002 decision 2).
const tokenPrefix = "K10"

// Token is a one-time join token bound to one node's name. CAHash lets the node check it reached the real control plane
// before it sends the secret.
type Token struct {
	CAHash string
	Node   v1.ObjectName
	Secret string
}

func (t Token) String() string {
	return tokenPrefix + t.CAHash + "::" + string(t.Node) + ":" + t.Secret
}

// ParseToken reads a token in its K10 form.
func ParseToken(s string) (Token, error) {
	const op = "multinode.ParseToken"
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), tokenPrefix)
	if !ok {
		return Token{}, fault.Invalidf(op, "a join token starts with %s", tokenPrefix)
	}
	hash, cred, ok := strings.Cut(rest, "::")
	if !ok {
		return Token{}, fault.Invalidf(op, "a join token reads K10<CA hash>::<node>:<secret>")
	}
	node, secret, ok := strings.Cut(cred, ":")
	if !ok || node == "" || secret == "" || len(hash) != sha256.Size*2 {
		return Token{}, fault.Invalidf(op, "a join token reads K10<CA hash>::<node>:<secret>")
	}
	return Token{CAHash: hash, Node: v1.ObjectName(node), Secret: secret}, nil
}

// newSecret draws a token's secret part.
func newSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fault.Wrapf(err, fault.Internal, "multinode.newSecret", "draw a token secret")
	}
	return hex.EncodeToString(b[:]), nil
}

// HashSecret is what the control plane keeps of a token's secret: its SHA-256, in hexadecimal.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// secretMatches compares a presented secret with a kept hash in constant time.
func secretMatches(secret, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashSecret(secret)), []byte(hash)) == 1
}
