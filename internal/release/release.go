// Package release parses Debian/Ubuntu repository Release files (and the verified
// body of an InRelease). A Release is the top-level index that lists every
// Packages/Sources/Contents/Translation file in a suite with its size and hashes.
// debswarm uses the SHA256 list to confirm that a fetched index matches a
// GPG-signed Release, anchoring the per-package hashes it trusts.
//
// Parsing is pure and does no verification; signature checking lives in
// internal/gpg. Callers must verify a Release's signature first, then parse the
// verified bytes with Parse.
package release

import (
	"bufio"
	"bytes"
	"errors"
	"strconv"
	"strings"
	"time"
)

// maxReleaseSize bounds the scanner so a hostile Release cannot exhaust memory.
// Real Release files run to a few MB (Debian main lists tens of thousands of
// by-hash entries); 32 MB is generous.
const maxReleaseSize = 32 * 1024 * 1024

// FileHash is a single index file's expected SHA256 and size as listed in Release.
type FileHash struct {
	SHA256 string
	Size   int64
}

// Release holds the fields debswarm consumes from a Release file.
type Release struct {
	Origin        string
	Suite         string
	Codename      string
	ValidUntil    time.Time // zero if absent or unparseable (freshness is left to APT)
	AcquireByHash bool
	// SHA256 maps a dist-relative index path (e.g. "main/binary-amd64/Packages.gz")
	// to its listed hash and size.
	SHA256 map[string]FileHash
}

// releaseTimeLayouts are the formats seen in Release "Valid-Until"/"Date" fields.
var releaseTimeLayouts = []string{
	"Mon, 02 Jan 2006 15:04:05 MST",
	"Mon, 02 Jan 2006 15:04:05 -0700",
	"02 Jan 2006 15:04:05 MST",
}

// ErrNoSHA256 is returned when a Release body has no SHA256 hash section — debswarm
// verifies against SHA256 only, so such a Release is unusable for verification.
var ErrNoSHA256 = errors.New("release: no SHA256 section found")

// Parse parses a Release body (or the verified plaintext of an InRelease). It
// returns ErrNoSHA256 if the body carries no SHA256 section.
func Parse(body []byte) (*Release, error) {
	r := &Release{SHA256: make(map[string]FileHash)}

	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), maxReleaseSize)

	inSHA256 := false
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}

		// Indented lines are entries of the current hash section. Only the SHA256
		// section is collected; MD5Sum/SHA1/SHA512 entries fall through ignored.
		if line[0] == ' ' || line[0] == '\t' {
			if inSHA256 {
				if f := strings.Fields(line); len(f) >= 3 {
					size, _ := strconv.ParseInt(f[1], 10, 64)
					r.SHA256[f[2]] = FileHash{SHA256: strings.ToLower(f[0]), Size: size}
				}
			}
			continue
		}

		// A top-level line ends any hash section and sets a "Key: value" field.
		inSHA256 = false
		key, val, ok := splitField(line)
		if !ok {
			continue
		}
		switch key {
		case "SHA256":
			inSHA256 = true // the following indented lines are SHA256 entries
		case "Origin":
			r.Origin = val
		case "Suite":
			r.Suite = val
		case "Codename":
			r.Codename = val
		case "Acquire-By-Hash":
			r.AcquireByHash = strings.EqualFold(val, "yes")
		case "Valid-Until":
			if t, err := parseReleaseTime(val); err == nil {
				r.ValidUntil = t
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(r.SHA256) == 0 {
		return nil, ErrNoSHA256
	}
	return r, nil
}

// splitField splits "Key: value" into its parts. ok is false for a line with no colon.
func splitField(line string) (key, val string, ok bool) {
	before, after, found := strings.Cut(line, ":")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(before), strings.TrimSpace(after), true
}

func parseReleaseTime(v string) (time.Time, error) {
	for _, layout := range releaseTimeLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("release: unrecognized time format")
}
