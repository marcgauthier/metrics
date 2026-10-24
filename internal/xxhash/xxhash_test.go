package xxhash

import (
	"bytes"
	"hash"
	"testing"
)

// Vectors captured from github.com/cespare/xxhash/v2 v2.3.0 before the fork.
func TestSum64UpstreamVectors(t *testing.T) {
	cases := []struct {
		in   []byte
		want uint64
	}{
		{nil, 17241709254077376921},
		{[]byte("a"), 15154266338359012955},
		{[]byte("hello"), 2794345569481354659},
		{bytes.Repeat([]byte("x"), 31), 6979749295578388976},
		{bytes.Repeat([]byte("y"), 32), 7267155159012462182},
		{bytes.Repeat([]byte("z"), 33), 14205499275651973736},
		{bytes.Repeat([]byte("0123456789abcdef"), 15), 17999602092709746514},
		{bytes.Repeat([]byte("q"), 1024), 4839225616679388587},
	}
	for _, tc := range cases {
		if got := Sum64(tc.in); got != tc.want {
			t.Errorf("Sum64(len=%d): got %d, want %d", len(tc.in), got, tc.want)
		}
		// Streaming digest must agree with the one-shot sum.
		d := New()
		for _, chunk := range [][]byte{tc.in[:len(tc.in)/2], tc.in[len(tc.in)/2:]} {
			if _, err := d.Write(chunk); err != nil {
				t.Fatal(err)
			}
		}
		if got := d.Sum64(); got != tc.want {
			t.Errorf("Digest.Sum64(len=%d): got %d, want %d", len(tc.in), got, tc.want)
		}
		var _ hash.Hash64 = d
		if d.Size() != 8 || d.BlockSize() != 32 {
			t.Errorf("Size=%d BlockSize=%d, want 8 and 32", d.Size(), d.BlockSize())
		}
	}
}
