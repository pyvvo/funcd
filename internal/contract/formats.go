package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// profileFormats are funcd's checks for the profile's format names (ADR-0301 Decision 1). Each replaces the
// engine's built-in of the same name. A check passes a value of another JSON type: a format constrains only its own
// type, and the type keyword reports a wrong type once.
func profileFormats() []*jsonschema.Format {
	return []*jsonschema.Format{
		{Name: "date-time", Validate: onString(checkDateTime)},
		{Name: "uuid", Validate: onString(checkUUID)},
		{Name: "email", Validate: onString(checkEmail)},
		{Name: "uri", Validate: onString(checkURI)},
		{Name: "int32", Validate: onInteger("int32", -1<<31, 1<<31-1)},
		{Name: "int64", Validate: onInteger("int64", -(1<<53 - 1), 1<<53-1)}, // ADR-0150: the JSON safe-integer range
	}
}

func onString(check func(string) error) func(interface{}) error {
	return func(v interface{}) error {
		if s, ok := v.(string); ok {
			return check(s)
		}
		return nil
	}
}

// onInteger checks an integer's range exactly, from its decimal digits: a number is never read through float64 or
// expanded to its full digits, so an exponent of any size costs nothing.
func onInteger(name string, lo, hi int64) func(interface{}) error {
	return func(v interface{}) error {
		n, ok := v.(json.Number)
		if !ok {
			return nil
		}
		x, isInt, fits := integerValue(string(n))
		if !isInt {
			return nil // a fraction fails the integer type, once
		}
		if !fits || x.Cmp(big.NewInt(lo)) < 0 || x.Cmp(big.NewInt(hi)) > 0 {
			return fmt.Errorf("not an %s: outside %d…%d", name, lo, hi)
		}
		return nil
	}
}

// integerValue reads a JSON number. isInt reports whether its value is an integer; fits whether that integer has at
// most 19 digits, in which case x holds it.
func integerValue(s string) (x *big.Int, isInt, fits bool) {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	mant, exp, _ := strings.Cut(strings.ToLower(s), "e")
	whole, frac, _ := strings.Cut(mant, ".")
	digits := strings.TrimLeft(whole+frac, "0")
	if digits == "" {
		return new(big.Int), true, true
	}
	// value = digits × 10^shift, where digits has no leading zero.
	shift := -len(frac)
	if exp != "" {
		e, ok := smallExponent(exp)
		if !ok { // |exponent| ≥ 10^6: a huge integer or a tiny fraction
			return nil, !strings.HasPrefix(exp, "-"), false
		}
		shift += e
	}
	if shift < 0 {
		cut := len(digits) + shift
		if cut <= 0 || strings.TrimRight(digits[cut:], "0") != "" {
			return nil, false, false
		}
		digits, shift = digits[:cut], 0
	}
	if len(digits)+shift > 19 {
		return nil, true, false
	}
	x, _ = new(big.Int).SetString(digits+strings.Repeat("0", shift), 10)
	if neg {
		x.Neg(x)
	}
	return x, true, true
}

// smallExponent parses a JSON exponent whose magnitude is below 10^6.
func smallExponent(s string) (int, bool) {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimLeft(strings.TrimLeft(s, "+-"), "0")
	if len(s) > 6 {
		return 0, false
	}
	n, ok := decimal(s)
	if s == "" {
		n, ok = 0, true
	}
	if neg {
		n = -n
	}
	return n, ok
}

// checkDateTime is RFC 3339 §5.6 date-time with §5.7's limits: T and Z in either case, a required offset, a day that
// exists in its month, and a leap second only at 23:59 UTC.
func checkDateTime(s string) error {
	bad := errors.New("not an RFC 3339 date-time")
	if len(s) < len("2006-01-02T15:04:05Z") || s[4] != '-' || s[7] != '-' || s[10] != 'T' && s[10] != 't' ||
		s[13] != ':' || s[16] != ':' {
		return bad
	}
	year, ok1 := decimal(s[0:4])
	month, ok2 := decimal(s[5:7])
	day, ok3 := decimal(s[8:10])
	hour, ok4 := decimal(s[11:13])
	minute, ok5 := decimal(s[14:16])
	second, ok6 := decimal(s[17:19])
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
		return bad
	}
	rest := s[19:]
	if strings.HasPrefix(rest, ".") {
		n := 1
		for n < len(rest) && isDigit(rest[n]) {
			n++
		}
		if n == 1 {
			return bad
		}
		rest = rest[n:]
	}
	offset := 0 // minutes east of UTC
	switch {
	case rest == "Z" || rest == "z":
	case len(rest) == 6 && (rest[0] == '+' || rest[0] == '-') && rest[3] == ':':
		oh, okh := decimal(rest[1:3])
		om, okm := decimal(rest[4:6])
		if !okh || !okm || oh > 23 || om > 59 {
			return bad
		}
		if offset = oh*60 + om; rest[0] == '-' {
			offset = -offset
		}
	default:
		return bad
	}
	if month < 1 || month > 12 || day < 1 || day > daysIn(year, month) || hour > 23 || minute > 59 || second > 60 {
		return bad
	}
	if utc := ((hour*60+minute-offset)%1440 + 1440) % 1440; second == 60 && utc != 23*60+59 {
		return bad
	}
	return nil
}

func daysIn(year, month int) int {
	switch month {
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// checkUUID is the RFC 4122 §3 string form: 8-4-4-4-12 hex digits in any case, with no prefix.
func checkUUID(s string) error {
	if len(s) != 36 {
		return errors.New("not an RFC 4122 uuid")
	}
	for i := range len(s) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if s[i] != '-' {
				return errors.New("not an RFC 4122 uuid")
			}
		} else if !isHex(s[i]) {
			return errors.New("not an RFC 4122 uuid")
		}
	}
	return nil
}

// checkEmail is an RFC 5321 §4.1.2 Mailbox: a Dot-string or Quoted-string local part, "@", then a Domain or an
// IPv4 or IPv6 address literal, in ASCII, within §4.5.3.1's lengths.
func checkEmail(s string) error {
	bad := errors.New("not an RFC 5321 mailbox")
	end := localPartEnd(s)
	if end < 0 || end >= len(s) || s[end] != '@' {
		return bad
	}
	local, domain := s[:end], s[end+1:]
	if len(local) > 64 || len(domain) > 255 {
		return bad
	}
	if strings.HasPrefix(domain, "[") {
		if !addressLiteral(domain) {
			return bad
		}
	} else if !isDomain(domain) {
		return bad
	}
	return nil
}

// localPartEnd returns the index just past s's local part, or -1 when s does not start with one.
func localPartEnd(s string) int {
	if !strings.HasPrefix(s, `"`) {
		at := strings.IndexByte(s, '@')
		if at < 0 || !isDotString(s[:at]) {
			return -1
		}
		return at
	}
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\': // quoted-pairSMTP
			if i++; i >= len(s) || s[i] < 32 || s[i] > 126 {
				return -1
			}
		case c == '"':
			return i + 1
		case c < 32 || c > 126:
			return -1
		}
	}
	return -1
}

func isDotString(s string) bool {
	if s == "" {
		return false
	}
	for _, atom := range strings.Split(s, ".") {
		if atom == "" || !allBytes(atom, isAtext) {
			return false
		}
	}
	return true
}

func isDomain(s string) bool {
	if s == "" {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || !isLetDig(label[0]) || !isLetDig(label[len(label)-1]) ||
			!allBytes(label, func(c byte) bool { return isLetDig(c) || c == '-' }) {
			return false
		}
	}
	return true
}

// addressLiteral is RFC 5321's IPv4 or IPv6 address literal; its "IPv6:" tag is case-insensitive like every ABNF
// string. A general address literal names a tag no standard registers, so it fails.
func addressLiteral(s string) bool {
	if len(s) < 2 || s[len(s)-1] != ']' {
		return false
	}
	in := s[1 : len(s)-1]
	if len(in) > 5 && strings.EqualFold(in[:5], "IPv6:") {
		return isIPv6(in[5:])
	}
	parts := strings.Split(in, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if n, ok := decimal(p); !ok || len(p) > 3 || n > 255 {
			return false
		}
	}
	return true
}

func isIPv6(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is6() && a.Zone() == ""
}

// checkURI is an RFC 3986 §3 URI: a scheme, then a hier-part, a query and a fragment of ASCII URI characters. A
// relative reference and an IRI (RFC 3987) fail.
func checkURI(s string) error {
	bad := errors.New("not an RFC 3986 uri")
	colon := strings.IndexByte(s, ':')
	if colon <= 0 || !isAlpha(s[0]) || !allBytes(s[:colon], func(c byte) bool {
		return isAlpha(c) || isDigit(c) || c == '+' || c == '-' || c == '.'
	}) {
		return bad
	}
	rest, fragment, hasFragment := strings.Cut(s[colon+1:], "#")
	rest, query, hasQuery := strings.Cut(rest, "?")
	if hasFragment && !uriChars(fragment, isQueryChar) || hasQuery && !uriChars(query, isQueryChar) {
		return bad
	}
	if auth, ok := strings.CutPrefix(rest, "//"); ok {
		path := ""
		if i := strings.IndexByte(auth, '/'); i >= 0 {
			auth, path = auth[:i], auth[i:]
		}
		if !isAuthority(auth) {
			return bad
		}
		rest = path
	}
	if !uriChars(rest, func(c byte) bool { return isPchar(c) || c == '/' }) {
		return bad
	}
	return nil
}

// isAuthority is RFC 3986 §3.2: [ userinfo "@" ] host [ ":" port ], the host an IP literal or a reg-name.
func isAuthority(a string) bool {
	if i := strings.LastIndexByte(a, '@'); i >= 0 {
		if !uriChars(a[:i], func(c byte) bool { return isUnreserved(c) || isSubDelim(c) || c == ':' }) {
			return false
		}
		a = a[i+1:]
	}
	port := ""
	if strings.HasPrefix(a, "[") {
		end := strings.IndexByte(a, ']')
		if end < 0 || !isIPLiteral(a[1:end]) {
			return false
		}
		if a = a[end+1:]; a != "" {
			if a[0] != ':' {
				return false
			}
			port = a[1:]
		}
	} else {
		host := a
		if i := strings.LastIndexByte(a, ':'); i >= 0 {
			host, port = a[:i], a[i+1:]
		}
		if !uriChars(host, func(c byte) bool { return isUnreserved(c) || isSubDelim(c) }) {
			return false
		}
	}
	return allBytes(port, isDigit)
}

// isIPLiteral is the inside of RFC 3986's IP-literal: an IPv6 address with no zone, or an IPvFuture.
func isIPLiteral(s string) bool {
	if v, ok := strings.CutPrefix(strings.ToLower(s), "v"); ok {
		ver, addr, ok := strings.Cut(v, ".")
		return ok && ver != "" && allBytes(ver, isHex) && addr != "" &&
			allBytes(addr, func(c byte) bool { return isUnreserved(c) || isSubDelim(c) || c == ':' })
	}
	return isIPv6(s)
}

// uriChars reports whether s holds only bytes that allowed accepts and percent-encoded octets.
func uriChars(s string, allowed func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
				return false
			}
			i += 2
		} else if !allowed(s[i]) {
			return false
		}
	}
	return true
}

func isPchar(c byte) bool     { return isUnreserved(c) || isSubDelim(c) || c == ':' || c == '@' }
func isQueryChar(c byte) bool { return isPchar(c) || c == '/' || c == '?' }
func isUnreserved(c byte) bool {
	return isAlpha(c) || isDigit(c) || c == '-' || c == '.' || c == '_' || c == '~'
}
func isSubDelim(c byte) bool { return strings.IndexByte("!$&'()*+,;=", c) >= 0 }
func isAtext(c byte) bool {
	return isAlpha(c) || isDigit(c) || strings.IndexByte("!#$%&'*+-/=?^_`{|}~", c) >= 0
}
func isLetDig(c byte) bool { return isAlpha(c) || isDigit(c) }
func isAlpha(c byte) bool  { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isHex(c byte) bool    { return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func allBytes(s string, ok func(byte) bool) bool {
	for i := range len(s) {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

// decimal parses ASCII digits; any other byte, or an empty s, fails.
func decimal(s string) (int, bool) {
	n := 0
	for i := range len(s) {
		if !isDigit(s[i]) {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, s != ""
}
