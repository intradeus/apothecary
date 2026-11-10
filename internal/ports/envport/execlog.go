package envport

import (
	"io"
	"unicode/utf8"
)

// DefaultExecLogRead is how much of the exec log one read returns when the
// caller names no size.
const DefaultExecLogRead = 64 << 10

// ExecLogChunk is one byte range of an exec log: Data is the bytes from
// Start to End, Size the log's whole length when it was read. A reader
// keeping up asks next from End; one going back asks for what ends at Start.
type ExecLogChunk struct {
	Data  []byte
	Start int64
	End   int64
	Size  int64
}

// ReadExecLogRange reads one range of a log of size bytes: up to n bytes
// from offset from, or the last n when from is negative (n <= 0 reads
// DefaultExecLogRead). Both edges move back to the start of a UTF-8
// character: a reader that splices ranges never splits one, and a
// character still being written comes with the next read. Every range is
// cut the same way, so where one ends the next begins.
func ReadExecLogRange(r io.ReaderAt, size, from, n int64) (ExecLogChunk, error) {
	if n <= 0 {
		n = DefaultExecLogRead
	}
	lo, hi := from, from+n
	if from < 0 {
		lo, hi = size-n, size
	}
	lo, hi = min(max(lo, 0), size), min(max(hi, 0), size)
	if hi < lo {
		hi = lo
	}
	// Three bytes on either side: a character is at most four.
	readLo := max(lo-(utf8.UTFMax-1), 0)
	buf := make([]byte, hi-readLo)
	if _, err := r.ReadAt(buf, readLo); err != nil && err != io.EOF {
		return ExecLogChunk{}, err
	}
	lo = runeStart(buf, readLo, lo)
	if hi > lo {
		if p := runeStart(buf, readLo, hi-1); p >= lo && !utf8.FullRune(buf[p-readLo:hi-readLo]) {
			hi = p
		}
	}
	return ExecLogChunk{Data: buf[lo-readLo : hi-readLo], Start: lo, End: hi, Size: size}, nil
}

// runeStart walks off — an offset into buf, which holds the log from base —
// back to the first byte of its character. An offset past buf (the end of
// the log), or a byte with no lead in reach, stays where it is.
func runeStart(buf []byte, base, off int64) int64 {
	if off-base >= int64(len(buf)) {
		return off
	}
	for p := off; p >= base && off-p < utf8.UTFMax; p-- {
		if utf8.RuneStart(buf[p-base]) {
			return p
		}
	}
	return off
}
