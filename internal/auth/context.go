package auth

import (
	"context"

	"github.com/mrcat71/waitformeet/internal/store"
)

type contextKey struct{ name string }

var (
	userKey    = contextKey{"user"}
	sessionKey = contextKey{"session"}
)

// WithUser attaches the signed-in user and their session to a request context.
func WithUser(ctx context.Context, user *store.User, sess *store.Session) context.Context {
	ctx = context.WithValue(ctx, userKey, user)
	return context.WithValue(ctx, sessionKey, sess)
}

// UserFrom returns the signed-in user, or nil for an anonymous visitor.
func UserFrom(ctx context.Context) *store.User {
	user, _ := ctx.Value(userKey).(*store.User)
	return user
}

// SessionFrom returns the current session, or nil for an anonymous visitor.
func SessionFrom(ctx context.Context) *store.Session {
	sess, _ := ctx.Value(sessionKey).(*store.Session)
	return sess
}

// IsSignedIn reports whether the request carries a valid session.
func IsSignedIn(ctx context.Context) bool {
	return UserFrom(ctx) != nil
}

// IsAdmin reports whether the signed-in user may manage users and visibility.
func IsAdmin(ctx context.Context) bool {
	user := UserFrom(ctx)
	return user != nil && user.IsAdmin
}

// CanSee reports whether the current visitor may view a section at the given
// visibility level.
//
// This is the single place the rule lives, so a new section cannot accidentally
// invent its own weaker interpretation.
func CanSee(ctx context.Context, level store.Visibility) bool {
	switch level {
	case store.VisPublic:
		return true
	case store.VisLoggedIn:
		return IsSignedIn(ctx)
	case store.VisAdmin:
		return IsAdmin(ctx)
	default:
		// An unrecognised level means the database holds something the code does
		// not understand. Showing the content would be the dangerous guess.
		return false
	}
}
