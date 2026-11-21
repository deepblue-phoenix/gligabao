// Package userpath encodes a user id into a path segment, for the stores that keep one directory (or key prefix) per user.
package userpath

import (
	"errors"
	"net/url"
	"strings"
)

// ErrEmptyUser is returned for an empty (or blank) user id.
var ErrEmptyUser = errors.New("the user id is required")

// Segment encodes a user id into exactly one safe path segment: injective, never empty, never "." or "..", and without "/".
func Segment(user string) (string, error) {
	if strings.TrimSpace(user) == "" {
		return "", ErrEmptyUser
	}
	switch enc := url.QueryEscape(user); enc {
	case ".":
		return "%2E", nil
	case "..":
		return "%2E%2E", nil
	default:
		return enc, nil
	}
}
