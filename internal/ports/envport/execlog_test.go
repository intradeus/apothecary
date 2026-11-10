package envport

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func readRange(t *testing.T, log string, from, n int64) ExecLogChunk {
	t.Helper()
	c, err := ReadExecLogRange(strings.NewReader(log), int64(len(log)), from, n)
	if err != nil {
		t.Fatal(err)
	}
	if got := log[c.Start:c.End]; string(c.Data) != got || c.Size != int64(len(log)) {
		t.Fatalf("chunk %+v does not match the log's [%d,%d) = %q", c, c.Start, c.End, got)
	}
	return c
}

func TestReadExecLogRange(t *testing.T) {
	const log = "0123456789"
	for _, c := range []struct {
		name          string
		from, n       int64
		want          string
		start, endOff int64
	}{
		{"the tail", -1, 4, "6789", 6, 10},
		{"a tail longer than the log", -1, 99, log, 0, 10},
		{"a range", 2, 3, "234", 2, 5},
		{"a range past the end", 8, 9, "89", 8, 10},
		{"from the end: nothing new", 10, 5, "", 10, 10},
		{"from past the end", 12, 5, "", 10, 10},
	} {
		got := readRange(t, log, c.from, c.n)
		if string(got.Data) != c.want || got.Start != c.start || got.End != c.endOff {
			t.Errorf("%s: %+v, want %q [%d,%d)", c.name, got, c.want, c.start, c.endOff)
		}
	}
	// No size reads the default; an empty log is an empty chunk.
	if got := readRange(t, strings.Repeat("x", DefaultExecLogRead+5), -1, 0); got.Start != 5 {
		t.Errorf("default read starts at %d", got.Start)
	}
	if got := readRange(t, "", -1, 10); len(got.Data) != 0 || got.End != 0 {
		t.Errorf("empty log = %+v", got)
	}
}

// Ranges never split a character, and spliced back to back they are the log.
func TestReadExecLogRangeKeepsCharactersWhole(t *testing.T) {
	log := "a─b😀c" + strings.Repeat("é", 5) + "─"
	// The tail and the range both start inside "😀" (4 bytes, at 5..9).
	if got := readRange(t, log, 6, 5); got.Start != 5 || !utf8.Valid(got.Data) {
		t.Errorf("a range from inside a character = %+v", got)
	}
	// A character cut at the end waits for the next read.
	if got := readRange(t, log, 0, 3); got.End != 1 || string(got.Data) != "a" {
		t.Errorf("a range ending inside a character = %+v", got)
	}
	for n := int64(1); n <= 6; n++ {
		var spliced []byte
		for from := int64(0); from < int64(len(log)); {
			c := readRange(t, log, from, n)
			if !utf8.Valid(c.Data) {
				t.Fatalf("n=%d from=%d: invalid %q", n, from, c.Data)
			}
			if c.End == from { // a character longer than n: read it whole
				c = readRange(t, log, from, utf8.UTFMax)
			}
			spliced = append(spliced, c.Data...)
			from = c.End
		}
		if string(spliced) != log {
			t.Fatalf("n=%d: spliced %q", n, spliced)
		}
	}
	// Going back: a range ending where another starts meets it exactly.
	tail := readRange(t, log, -1, 7)
	before := readRange(t, log, tail.Start-6, 6)
	if before.End != tail.Start || string(before.Data)+string(tail.Data) != log[before.Start:] {
		t.Errorf("back to back: %+v then %+v", before, tail)
	}
	// Bytes that are no character at all are passed on as they are.
	junk := "ab\x80\x80\x80\x80\x80cd"
	if got := readRange(t, junk, 6, 3); got.Start != 6 {
		t.Errorf("no lead in reach = %+v", got)
	}
}

type failingReader struct{}

func (failingReader) ReadAt([]byte, int64) (int, error) { return 0, errors.New("disk gone") }

func TestReadExecLogRangeReportsAFailedRead(t *testing.T) {
	if _, err := ReadExecLogRange(failingReader{}, 10, 0, 5); err == nil {
		t.Error("a failed read went unreported")
	}
	if _, err := ReadExecLogRange(bytes.NewReader(nil), 0, 0, 5); err != nil {
		t.Errorf("an empty log: %v", err)
	}
}
