package auth

import "context"

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

type Credentials struct {
	Email    string
	Password string
}

type Principal struct {
	ID    string
	Email string
	Role  Role
}

type SessionToken string

type InviteLink struct {
	URL       string
	ExpiresAt int64
}

type User struct {
	ID    string
	Email string
	Role  Role
}

type AuthProvider interface {
	Authenticate(ctx context.Context, creds Credentials) (Principal, error)
	Principal(ctx context.Context, session SessionToken) (Principal, error)
	ListUsers(ctx context.Context) ([]User, error)
	InviteUser(ctx context.Context, email string, role Role) (InviteLink, error)
	RevokeUser(ctx context.Context, userID string) error
}
