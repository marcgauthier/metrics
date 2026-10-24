package zstd

import (
	"bytes"
	"io"
	"testing"
)

func TestForkRoundTrip(t *testing.T) {
	inputs := [][]byte{
		nil,
		[]byte("hello world"),
		bytes.Repeat([]byte("compressible-data-0123456789;"), 4000),
	}
	for i, in := range inputs {
		var enc *Encoder
		var err error
		if i == 2 {
			enc, err = NewWriter(nil, WithEncoderLevel(EncoderLevelFromZstd(3)))
		} else {
			enc, err = NewWriter(nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		out := enc.EncodeAll(in, nil)
		if err := enc.Close(); err != nil {
			t.Fatal(err)
		}
		if len(in) > 0 && len(out) == 0 {
			t.Fatal("empty output for non-empty input")
		}
		dec, err := NewReader(nil)
		if err != nil {
			t.Fatal(err)
		}
		back, err := dec.DecodeAll(out, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(back, in) {
			t.Fatalf("case %d: round trip mismatch (in=%d, out=%d)", i, len(in), len(back))
		}
		dec.Close()
	}
}

func TestForkStreamingAndDeterminism(t *testing.T) {
	in := bytes.Repeat([]byte("streaming-payload-"), 2000)
	var buf1, buf2 bytes.Buffer
	for _, w := range []io.Writer{&buf1, &buf2} {
		enc, err := NewWriter(w)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := enc.Write(in); err != nil {
			t.Fatal(err)
		}
		if err := enc.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Fatal("encoder output is not deterministic")
	}
	dec, err := NewReader(&buf1)
	if err != nil {
		t.Fatal(err)
	}
	back, err := io.ReadAll(dec)
	if err != nil {
		t.Fatal(err)
	}
	dec.Close()
	if !bytes.Equal(back, in) {
		t.Fatal("streaming round trip mismatch")
	}
}
