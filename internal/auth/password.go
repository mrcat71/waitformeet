// Package auth handles signing in: passwords, sessions, CSRF tokens and OIDC.
package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost trades login latency for resistance to offline cracking. Cost 12 is
// roughly a quarter of a second on current hardware, which nobody notices on a site
// people sign in to once a month.
const bcryptCost = 12

const (
	// MinPasswordLen is deliberately longer than the usual eight. This site has no
	// account recovery and a handful of users, so a decent minimum costs nothing.
	MinPasswordLen = 12
	// MaxPasswordLen is bcrypt's hard limit. Anything longer is silently truncated
	// by the algorithm, which would make two different passwords interchangeable,
	// so it is rejected outright instead.
	MaxPasswordLen = 72
)

var (
	// ErrPasswordTooShort reports a password below MinPasswordLen.
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	// ErrPasswordTooLong reports a password past bcrypt's 72-byte input limit.
	ErrPasswordTooLong = fmt.Errorf("password must be at most %d bytes", MaxPasswordLen)
	// ErrNoPassword reports an account that cannot be used with a password, either
	// because none was ever set or because it signs in through the identity provider.
	ErrNoPassword = errors.New("this account has no password set")
	// ErrBadCredentials reports a failed sign-in. It is deliberately vague: telling
	// an attacker whether the address exists is free reconnaissance.
	ErrBadCredentials = errors.New("incorrect email or password")
)

// ValidatePassword checks a new password against the length rules.
//
// The minimum counts characters and the maximum counts bytes, on purpose. Twelve
// Chinese characters are a perfectly good password but thirty-six bytes, so the
// minimum must not punish them; bcrypt's ceiling, meanwhile, is genuinely a byte
// limit and has to be checked as one.
func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLen {
		return ErrPasswordTooShort
	}
	if len(password) > MaxPasswordLen {
		return ErrPasswordTooLong
	}
	return nil
}

// HashPassword validates and hashes a new password.
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword compares a candidate password against a stored hash.
//
// It returns ErrBadCredentials for both a wrong password and a malformed hash, so
// callers cannot accidentally leak which one it was.
func VerifyPassword(hash, password string) error {
	if strings.TrimSpace(hash) == "" {
		return ErrNoPassword
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return ErrBadCredentials
	}
	return nil
}

// DummyVerify burns roughly the same time as a real bcrypt comparison.
//
// Without it, a request for an address that does not exist would return noticeably
// faster than one for an address that does, which turns the login form into a user
// enumeration oracle.
func DummyVerify(password string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
}

// dummyHash is generated at startup from random bytes rather than hardcoded, so it
// is a genuinely valid hash at exactly the current cost, and no password can ever
// match it. The one-off cost is a single bcrypt round at process start.
var dummyHash = mustDummyHash()

func mustDummyHash() []byte {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		panic("auth: no usable source of randomness: " + err.Error())
	}
	hash, err := bcrypt.GenerateFromPassword(secret[:], bcryptCost)
	if err != nil {
		panic("auth: cannot generate the timing-equalising hash: " + err.Error())
	}
	return hash
}
