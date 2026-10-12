package main

// Where the names come from. Two line formats are accepted:
//   - dnsmasq/FTL query lines: "... query[A] example.com from 10.0.0.2"
//   - one bare name per line (used by the gates)
// A file is followed like `tail -F`: new lines are read as they are appended,
// and a file that shrinks or is replaced (log rotation) is reopened.

import (
	"bufio"
	"io"
	"os"
	"strings"
	"time"
)

func nameFromLine(l string) (string, bool) {
	l = strings.TrimSpace(l)
	if l == "" || strings.HasPrefix(l, "#") {
		return "", false
	}
	if i := strings.Index(l, "query["); i >= 0 {
		f := strings.Fields(l[i:])
		if len(f) < 2 {
			return "", false
		}
		qt := strings.TrimSuffix(strings.TrimPrefix(f[0], "query["), "]")
		if qt != "A" && qt != "AAAA" && qt != "HTTPS" {
			return "", false
		}
		return f[1], true
	}
	if strings.ContainsAny(l, " \t") {
		return "", false
	}
	return l, true
}

func readAll(r io.Reader, fn func(string)) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if n, ok := nameFromLine(sc.Text()); ok {
			fn(n)
		}
	}
}

// follow tails path forever, starting at its current end.
func follow(path string, fn func(string)) {
	var f *os.File
	var rd *bufio.Reader
	var pos int64
	whence := io.SeekEnd // first open: only new lookups; after rotation: the new file from its start
	for {
		if f == nil {
			var err error
			if f, err = os.Open(path); err != nil {
				time.Sleep(2 * time.Second)
				continue
			}
			pos, _ = f.Seek(0, whence)
			whence = io.SeekStart
			rd = bufio.NewReader(f)
		}
		line, err := rd.ReadString('\n')
		if err == nil {
			pos += int64(len(line))
			if n, ok := nameFromLine(line); ok {
				fn(n)
			}
			continue
		}
		// At EOF: reopen if the file was rotated or truncated.
		time.Sleep(time.Second)
		st, serr := os.Stat(path)
		fst, ferr := f.Stat()
		if serr != nil || ferr != nil || !os.SameFile(st, fst) || st.Size() < pos {
			f.Close()
			f = nil
			continue
		}
		rd.Reset(io.MultiReader(strings.NewReader(line), f))
	}
}
