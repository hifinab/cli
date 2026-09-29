package main

import (
	"errors"
	"io"
	"os"

	"golang.org/x/term"
)

// readSecret reads a key or token from the terminal without showing it, but
// prints a * for each character so a paste is visibly received. The caller
// ends the line.
func readSecret(file *os.File, echo io.Writer) ([]byte, error) {
	state, err := term.MakeRaw(int(file.Fd()))
	if err != nil {
		return term.ReadPassword(int(file.Fd()))
	}
	defer term.Restore(int(file.Fd()), state)
	return readMasked(file, echo)
}

// readMasked handles the keys of a masked prompt: Enter ends it, Backspace
// deletes, Ctrl-C and Ctrl-D cancel, and escape sequences such as arrow keys
// and bracketed-paste markers are ignored.
func readMasked(r io.Reader, echo io.Writer) ([]byte, error) {
	var secret []byte
	var one [1]byte
	escape := 0 // 1 after ESC, 2 inside a CSI sequence
	for {
		n, err := r.Read(one[:])
		if n == 0 {
			if err == nil {
				continue
			}
			if errors.Is(err, io.EOF) && len(secret) > 0 {
				return secret, nil
			}
			return nil, err
		}
		c := one[0]
		switch {
		case escape == 1:
			escape = 0
			if c == '[' {
				escape = 2
			}
		case escape == 2:
			if c >= 0x40 && c <= 0x7e {
				escape = 0
			}
		case c == 0x1b:
			escape = 1
		case c == '\r' || c == '\n':
			return secret, nil
		case c == 3 || c == 4:
			return nil, errors.New("cancelled")
		case c == 127 || c == 8:
			if len(secret) > 0 {
				secret = secret[:len(secret)-1]
				io.WriteString(echo, "\b \b")
			}
		case c >= 0x20:
			secret = append(secret, c)
			io.WriteString(echo, "*")
		}
	}
}
