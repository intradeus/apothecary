package auth

import "context"

// Principal is who a request is: the user, and how they proved it. Exactly
// one of Session (a browser cookie) and Token (a personal token) is set.
type Principal struct {
	User    User
	Session *Session
	Token   *Token
}

// ViaCookie reports a browser session, the one case that needs same-origin
// checks on writes: a cookie rides along on a cross-site request, a bearer
// header does not.
func (p Principal) ViaCookie() bool { return p.Session != nil }

type ctxKey struct{}

// WithPrincipal attaches the request's principal to a context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalFrom reads the principal the request was authenticated as.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// UserID is the principal's user id, or "" outside an authenticated request.
func UserID(ctx context.Context) string {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return ""
	}
	return p.User.ID
}
