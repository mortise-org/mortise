package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/MC-Meesh/mortise/internal/constants"
)

const (
	userLabelKey     = "mortise.dev/user"
	inviteLabelKey   = "mortise.dev/invite"
	inviteExpiryDays = 7
)

type NativeAuthProvider struct {
	client client.Client
	jwt    *JWTHelper
}

func NewNativeAuthProvider(c client.Client) *NativeAuthProvider {
	return &NativeAuthProvider{
		client: c,
		jwt:    NewJWTHelper(c),
	}
}

func userSecretName(email string) string {
	return "user-" + hex.EncodeToString([]byte(email))
}

func inviteSecretName(email string) string {
	return "invite-" + hex.EncodeToString([]byte(email))
}

func (n *NativeAuthProvider) Authenticate(ctx context.Context, creds Credentials) (Principal, error) {
	var secret corev1.Secret
	err := n.client.Get(ctx, types.NamespacedName{
		Name:      userSecretName(creds.Email),
		Namespace: namespace,
	}, &secret)
	if errors.IsNotFound(err) {
		return Principal{}, fmt.Errorf("invalid credentials")
	}
	if err != nil {
		return Principal{}, fmt.Errorf("reading user secret: %w", err)
	}

	hash := secret.Data["password_hash"]
	if err := bcrypt.CompareHashAndPassword(hash, []byte(creds.Password)); err != nil {
		return Principal{}, fmt.Errorf("invalid credentials")
	}

	return Principal{
		ID:    string(secret.Data["email"]),
		Email: string(secret.Data["email"]),
		Role:  Role(secret.Data["role"]),
	}, nil
}

func (n *NativeAuthProvider) Principal(ctx context.Context, session SessionToken) (Principal, error) {
	return n.jwt.ValidateToken(ctx, string(session))
}

func (n *NativeAuthProvider) ListUsers(ctx context.Context) ([]User, error) {
	var secrets corev1.SecretList
	err := n.client.List(ctx, &secrets,
		client.InNamespace(namespace),
		client.MatchingLabels{userLabelKey: "true"},
	)
	if err != nil {
		return nil, fmt.Errorf("listing user secrets: %w", err)
	}

	users := make([]User, 0, len(secrets.Items))
	for _, s := range secrets.Items {
		users = append(users, User{
			ID:    string(s.Data["email"]),
			Email: string(s.Data["email"]),
			Role:  Role(s.Data["role"]),
		})
	}
	return users, nil
}

func (n *NativeAuthProvider) InviteUser(ctx context.Context, email string, role Role) (InviteLink, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return InviteLink{}, fmt.Errorf("generating invite token: %w", err)
	}

	expiresAt := time.Now().Add(inviteExpiryDays * 24 * time.Hour)
	inviteToken := hex.EncodeToString(token)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      inviteSecretName(email),
			Namespace: namespace,
			Labels: map[string]string{
				inviteLabelKey: "true",
			},
		},
		Data: map[string][]byte{
			"email":      []byte(email),
			"role":       []byte(role),
			"token":      []byte(inviteToken),
			"expires_at": fmt.Appendf(nil, "%d", expiresAt.Unix()),
		},
	}

	if err := n.client.Create(ctx, secret); err != nil {
		return InviteLink{}, fmt.Errorf("creating invite secret: %w", err)
	}

	return InviteLink{
		URL:       fmt.Sprintf("/invite?token=%s", inviteToken),
		ExpiresAt: expiresAt.Unix(),
	}, nil
}

func (n *NativeAuthProvider) RevokeUser(ctx context.Context, userID string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      userSecretName(userID),
			Namespace: namespace,
		},
	}
	if err := n.client.Delete(ctx, secret); err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("user not found: %s", userID)
		}
		return fmt.Errorf("deleting user secret: %w", err)
	}
	return nil
}

// CreateUser stores a new user in a k8s Secret. Used during invite acceptance.
func (n *NativeAuthProvider) CreateUser(ctx context.Context, email, password string, role Role) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      userSecretName(email),
			Namespace: namespace,
			Labels: map[string]string{
				userLabelKey: "true",
			},
		},
		Data: map[string][]byte{
			"email":         []byte(email),
			"password_hash": hash,
			"role":          []byte(role),
			"team_ref":      []byte(constants.DefaultTeamName),
		},
	}

	if err := n.client.Create(ctx, secret); err != nil {
		return fmt.Errorf("creating user secret: %w", err)
	}
	return nil
}

// GenerateSessionToken creates a JWT for the given principal.
func (n *NativeAuthProvider) GenerateSessionToken(ctx context.Context, p Principal) (SessionToken, error) {
	token, err := n.jwt.GenerateToken(ctx, p)
	if err != nil {
		return "", err
	}
	return SessionToken(token), nil
}
