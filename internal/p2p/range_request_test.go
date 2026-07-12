package p2p

import (
	"bytes"
	"strings"
	"testing"
)

// Range requests are a fixed binary frame, not newline-delimited: the big-endian
// offsets can contain the newline byte (0x0A). encode/decode must round-trip such
// offsets. Regression for the bug where the server read the frame with
// ReadBytes('\n') and truncated any offset containing 0x0A (files > ~160 MB),
// silently failing those chunks over P2P.
func TestRangeRequest_RoundTrip(t *testing.T) {
	hash := strings.Repeat("a", 64)

	const chunk = 4 * 1024 * 1024
	cases := []struct {
		name       string
		start, end int64
		wantEnd    int64 // end after to-EOF normalization
	}{
		// 40 * 4 MiB = 0x0A000000 → big-endian bytes 00 00 00 00 0A 00 00 00.
		{"offset with 0x0A byte (chunk 40 of a large file)", 40 * chunk, 41 * chunk, 41 * chunk},
		{"both offsets contain 0x0A", 0x0A0A, 0x0A0A00, 0x0A0A00},
		{"to-EOF end (-1 encoded as 0)", 40 * chunk, -1, 0},
		{"zero range", 0, 0, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			frame := encodeRangeRequest(hash, c.start, c.end)
			if len(frame) != rangeRequestLen {
				t.Fatalf("frame length = %d, want %d", len(frame), rangeRequestLen)
			}

			gotHash, gotStart, gotEnd, err := decodeRangeRequest(bytes.NewReader(frame))
			if err != nil {
				t.Fatalf("decodeRangeRequest: %v", err)
			}
			if gotHash != hash {
				t.Errorf("hash mismatch: got %q", gotHash)
			}
			if gotStart != c.start {
				t.Errorf("start = %d, want %d", gotStart, c.start)
			}
			if gotEnd != c.wantEnd {
				t.Errorf("end = %d, want %d", gotEnd, c.wantEnd)
			}
		})
	}
}

// Documents the hazard the fixed-size decode guards against: the encoded frame
// for a 0x0A offset really does contain a newline byte before the terminator, so
// a newline-delimited read would have stopped early.
func TestRangeRequest_OffsetContainsNewlineByte(t *testing.T) {
	frame := encodeRangeRequest(strings.Repeat("a", 64), 40*4*1024*1024, 0)
	// Everything before the trailing terminator is the hash + binary offsets.
	body := frame[:rangeRequestLen-1]
	if !bytes.ContainsRune(body, '\n') {
		t.Fatal("expected the encoded offset to contain a 0x0A byte for this test to be meaningful")
	}
	// Decode must still recover the full offset despite the embedded newline.
	_, start, _, err := decodeRangeRequest(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if start != 40*4*1024*1024 {
		t.Errorf("start = %d, want %d — embedded 0x0A truncated the frame", start, 40*4*1024*1024)
	}
}

// A short/truncated frame must be a clean error, not a partial parse.
func TestRangeRequest_TruncatedFrame(t *testing.T) {
	frame := encodeRangeRequest(strings.Repeat("a", 64), 1024, 2048)
	if _, _, _, err := decodeRangeRequest(bytes.NewReader(frame[:40])); err == nil {
		t.Error("expected error decoding a truncated frame, got nil")
	}
}
