package loglimit

import (
	"bytes"
	"testing"
)

func TestExactBound(t *testing.T) {
	var out bytes.Buffer
	w := Writer{Out: &out, Limit: 1 << 20}
	input := bytes.Repeat([]byte("x"), 2<<20)
	if n, err := w.Write(input); n != len(input) || err != nil {
		t.Fatal(n, err)
	}
	w.Write(input)
	if out.Len() != 1<<20 || bytes.Count(out.Bytes(), []byte("log truncated")) != 1 {
		t.Fatal(out.Len())
	}
}
