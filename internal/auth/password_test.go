package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  error
	}{
		{name: "long enough", password: "correct horse battery"},
		{name: "exactly the minimum", password: strings.Repeat("a", MinPasswordLen)},
		{name: "one short", password: strings.Repeat("a", MinPasswordLen-1), wantErr: ErrPasswordTooShort},
		{name: "empty", password: "", wantErr: ErrPasswordTooShort},
		{
			// Twelve Chinese characters are a fine password but thirty-six bytes.
			// Counting bytes for the minimum would wrongly accept far fewer of them.
			name:     "twelve non-ascii characters are enough",
			password: "密码密码密码密码密码密码",
		},
		{
			name:     "eleven non-ascii characters are not enough",
			password: "密码密码密码密码密码密",
			wantErr:  ErrPasswordTooShort,
		},
		{
			// bcrypt silently truncates past 72 bytes, which would make two
			// different long passwords interchangeable.
			name:     "past bcrypt's byte limit",
			password: strings.Repeat("a", MaxPasswordLen+1),
			wantErr:  ErrPasswordTooLong,
		},
		{name: "exactly bcrypt's byte limit", password: strings.Repeat("a", MaxPasswordLen)},
		{
			name:     "non-ascii past the byte limit",
			password: strings.Repeat("密", 25), // 75 bytes
			wantErr:  ErrPasswordTooLong,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePassword(tt.password)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidatePassword() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestHashAndVerifyPassword(t *testing.T) {
	const password = "correct horse battery staple"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if hash == password {
		t.Fatal("HashPassword() returned the password unchanged")
	}

	if err := VerifyPassword(hash, password); err != nil {
		t.Errorf("VerifyPassword(correct) error = %v, want nil", err)
	}
	if err := VerifyPassword(hash, "something else entirely"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("VerifyPassword(wrong) error = %v, want ErrBadCredentials", err)
	}
}

// The same password must not produce the same hash twice, or the salt is not doing
// its job and identical passwords would be visible as identical rows.
func TestHashPasswordIsSalted(t *testing.T) {
	const password = "correct horse battery staple"

	first, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if first == second {
		t.Error("hashing the same password twice produced identical output")
	}
}

func TestVerifyPasswordRejections(t *testing.T) {
	tests := []struct {
		name    string
		hash    string
		want    error
		attempt string
	}{
		{name: "no password set", hash: "", want: ErrNoPassword, attempt: "anything"},
		{name: "whitespace hash", hash: "   ", want: ErrNoPassword, attempt: "anything"},
		{
			// A corrupt hash must read as a failed login, never as a success.
			name:    "malformed hash",
			hash:    "not-a-bcrypt-hash",
			want:    ErrBadCredentials,
			attempt: "anything",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := VerifyPassword(tt.hash, tt.attempt); !errors.Is(err, tt.want) {
				t.Errorf("VerifyPassword() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDummyVerifyNeverSucceeds(t *testing.T) {
	// It has no return value by design; the point is only that it does not panic
	// and burns comparable time. Calling it must be safe with any input.
	for _, attempt := range []string{"", "password", strings.Repeat("x", 100)} {
		DummyVerify(attempt)
	}
}
