// Copyright 2015 The DER ASCII Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/der-ascii/lib"
)

// A position describes a location in the input stream.
type position struct {
	Offset int // offset, starting at 0
	Line   int // line number, starting at 1
	Column int // column number, starting at 1 (byte count)
}

// A tokenKind is a kind of token.
type tokenKind int

const (
	tokenBytes tokenKind = iota
	tokenLeftCurly
	tokenRightCurly
	tokenEOF
)

// A parseError is an error during parsing DER ASCII.
type parseError struct {
	Pos position
	Err error
}

func (t *parseError) Error() string {
	return fmt.Sprintf("line %d: %s", t.Pos.Line, t.Err)
}

// A token is a token in a DER ASCII file.
type token struct {
	// Kind is the kind of the token.
	Kind tokenKind
	// Value, for a tokenBytes token, is the decoded value of the token in
	// bytes.
	Value []byte
	// Pos is the position of the first byte of the token.
	Pos position
}

var (
	regexpInteger = regexp.MustCompile(`^-?[0-9]+$`)
	regexpOID     = regexp.MustCompile(`^[0-9]+(\.[0-9]+)+$`)
)

type scanner struct {
	text string
	pos  position
}

func newScanner(text string) *scanner {
	return &scanner{text: text, pos: position{Line: 1}}
}

func (s *scanner) Next() (token, error) {
again:
	if s.isEOF() {
		return token{Kind: tokenEOF, Pos: s.pos}, nil
	}

	switch s.text[s.pos.Offset] {
	case ' ', '\t', '\n', '\r':
		// Skip whitespace.
		s.advance()
		goto again
	case '#':
		// Skip to the end of the comment.
		s.advance()
		for !s.isEOF() {
			wasNewline := s.text[s.pos.Offset] == '\n'
			s.advance()
			if wasNewline {
				break
			}
		}
		goto again
	case '{':
		s.advance()
		return token{Kind: tokenLeftCurly, Pos: s.pos}, nil
	case '}':
		s.advance()
		return token{Kind: tokenRightCurly, Pos: s.pos}, nil
	case '"':
		s.advance()
		start := s.pos
		var bytes []byte
		for {
			if s.isEOF() {
				return token{}, &parseError{start, errors.New("unmatched \"")}
			}
			switch c := s.text[s.pos.Offset]; c {
			case '"':
				s.advance()
				return token{Kind: tokenBytes, Value: bytes, Pos: start}, nil
			case '\\':
				s.advance()
				if s.isEOF() {
					return token{}, &parseError{s.pos, errors.New("expected escape character")}
				}
				switch c2 := s.text[s.pos.Offset]; c2 {
				case 'n':
					bytes = append(bytes, '\n')
				case '"', '\\':
					bytes = append(bytes, c2)
				case 'x':
					s.advance()
					if s.pos.Offset+2 > len(s.text) {
						return token{}, &parseError{s.pos, errors.New("unfinished escape sequence")}
					}
					b, err := hex.DecodeString(s.text[s.pos.Offset : s.pos.Offset+2])
					if err != nil {
						return token{}, &parseError{s.pos, err}
					}
					bytes = append(bytes, b[0])
					s.advance()
				default:
					return token{}, &parseError{s.pos, fmt.Errorf("unknown escape sequence \\%c", c2)}
				}
			default:
				bytes = append(bytes, c)
			}
			s.advance()
		}
	case '`':
		s.advance()
		hexStr, ok := s.consumeUpTo('`')
		if !ok {
			return token{}, &parseError{s.pos, errors.New("unmatched `")}
		}
		bytes, err := hex.DecodeString(hexStr)
		if err != nil {
			return token{}, &parseError{s.pos, err}
		}
		return token{Kind: tokenBytes, Value: bytes, Pos: s.pos}, nil
	case '[':
		s.advance()
		tagStr, ok := s.consumeUpTo(']')
		if !ok {
			return token{}, &parseError{s.pos, errors.New("unmatched [")}
		}
		tag, err := decodeTagString(tagStr)
		if err != nil {
			return token{}, &parseError{s.pos, err}
		}
		return token{Kind: tokenBytes, Value: appendTag(nil, tag), Pos: s.pos}, nil
	}

	// Normal token. Consume up to the next whitespace character, symbol, or
	// EOF.
	start := s.pos
	s.advance()
loop:
	for !s.isEOF() {
		switch s.text[s.pos.Offset] {
		case ' ', '\t', '\n', '\r', '{', '}', '[', ']', '`', '"', '#':
			break loop
		default:
			s.advance()
		}
	}

	symbol := s.text[start.Offset:s.pos.Offset]

	// See if it is a tag.
	tag, ok := lib.TagByName(symbol)
	if ok {
		return token{Kind: tokenBytes, Value: appendTag(nil, tag), Pos: start}, nil
	}

	if regexpInteger.MatchString(symbol) {
		value, err := strconv.ParseInt(symbol, 10, 64)
		if err != nil {
			return token{}, &parseError{start, err}
		}
		return token{Kind: tokenBytes, Value: appendInteger(nil, value), Pos: s.pos}, nil
	}

	if regexpOID.MatchString(symbol) {
		oidStr := strings.Split(symbol, ".")
		var oid []uint32
		for _, s := range oidStr {
			u, err := strconv.ParseUint(s, 10, 32)
			if err != nil {
				return token{}, &parseError{start, err}
			}
			oid = append(oid, uint32(u))
		}
		der, ok := appendObjectIdentifier(nil, oid)
		if !ok {
			return token{}, errors.New("invalid OID")
		}
		return token{Kind: tokenBytes, Value: der, Pos: s.pos}, nil
	}

	return token{}, fmt.Errorf("unrecognized symbol '%s'", symbol)
}

func (s *scanner) isEOF() bool {
	return s.pos.Offset >= len(s.text)
}

func (s *scanner) advance() {
	if !s.isEOF() {
		if s.text[s.pos.Offset] == '\n' {
			s.pos.Line++
			s.pos.Column = 0
		} else {
			s.pos.Column++
		}
		s.pos.Offset++
	}
}

func (s *scanner) consumeUpTo(b byte) (string, bool) {
	start := s.pos.Offset
	for !s.isEOF() {
		if s.text[s.pos.Offset] == b {
			ret := s.text[start:s.pos.Offset]
			s.advance()
			return ret, true
		}
		s.advance()
	}
	return "", false
}

func asciiToDERImpl(scanner *scanner, leftCurly *token) ([]byte, error) {
	var out []byte
	for {
		token, err := scanner.Next()
		if err != nil {
			return nil, err
		}
		switch token.Kind {
		case tokenBytes:
			out = append(out, token.Value...)
		case tokenLeftCurly:
			child, err := asciiToDERImpl(scanner, &token)
			if err != nil {
				return nil, err
			}
			out = appendLength(out, len(child))
			out = append(out, child...)
		case tokenRightCurly:
			if leftCurly != nil {
				return out, nil
			}
			return nil, &parseError{token.Pos, errors.New("unmatched '}'")}
		case tokenEOF:
			if leftCurly == nil {
				return out, nil
			}
			return nil, &parseError{leftCurly.Pos, errors.New("unmatched '{'")}
		default:
			panic(token)
		}
	}
}

func asciiToDER(input string) ([]byte, error) {
	scanner := newScanner(input)
	return asciiToDERImpl(scanner, nil)
}
