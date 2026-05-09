package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	userLabelKey     = "mortise.dev/user"
	inviteLabelKey   = "mortise.dev/invite"
	inviteExpiryDays = 7
)

var (
	ErrUserNotFound              = stderrors.New("user not found")
	ErrPasswordChangeInvalidated = stderrors.New("session invalidated by password change")
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

	return principalFromSecret(&secret), nil
}

func (n *NativeAuthProvider) Principal(ctx context.Context, session SessionToken) (Principal, error) {
	principal, tokenGen, err := n.jwt.ValidateToken(ctx, string(session))
	if err != nil {
		return Principal{}, err
	}

	return n.CurrentPrincipal(ctx, principal.Email, tokenGen)
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

// CurrentPrincipal reloads the current native-auth user state and confirms the
// token was not invalidated by a password reset.
func (n *NativeAuthProvider) CurrentPrincipal(ctx context.Context, email string, tokenGen int64) (Principal, error) {
	var secret corev1.Secret
	err := n.client.Get(ctx, types.NamespacedName{
		Name:      userSecretName(email),
		Namespace: namespace,
	}, &secret)
	if errors.IsNotFound(err) {
		return Principal{}, ErrUserNotFound
	}
	if err != nil {
		return Principal{}, fmt.Errorf("reading user secret: %w", err)
	}

	if tokenGen < passwordGenFromSecret(&secret) {
		return Principal{}, ErrPasswordChangeInvalidated
	}

	return principalFromSecret(&secret), nil
}

// CheckPasswordGen verifies that the token's password generation matches the
// current value for the user. Returns an error if the password was changed
// after the token was issued.
func (n *NativeAuthProvider) CheckPasswordGen(ctx context.Context, email string, tokenGen int64) error {
	_, err := n.CurrentPrincipal(ctx, email, tokenGen)
	return err
}

// CreateUser stores a new user in a k8s Secret. Used during invite acceptance.
func (n *NativeAuthProvider) CreateUser(ctx context.Context, email, password string, role Role) error {
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
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
			"password_gen":  []byte("0"),
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

// UpdatePassword replaces the password hash for an existing user.
func (n *NativeAuthProvider) UpdatePassword(ctx context.Context, email, newPassword string) error {
	if len(newPassword) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}

	var secret corev1.Secret
	err := n.client.Get(ctx, types.NamespacedName{
		Name:      userSecretName(email),
		Namespace: namespace,
	}, &secret)
	if errors.IsNotFound(err) {
		return fmt.Errorf("user not found or password could not be updated")
	}
	if err != nil {
		return fmt.Errorf("reading user secret: %w", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	secret.Data["password_hash"] = hash

	var gen int64
	if raw, ok := secret.Data["password_gen"]; ok {
		fmt.Sscanf(string(raw), "%d", &gen)
	}
	gen++
	secret.Data["password_gen"] = fmt.Appendf(nil, "%d", gen)

	if err := n.client.Update(ctx, &secret); err != nil {
		return fmt.Errorf("updating user secret: %w", err)
	}
	return nil
}

// VerifyPassword checks a plaintext password against the stored hash for a user.
func (n *NativeAuthProvider) VerifyPassword(ctx context.Context, email, password string) error {
	var secret corev1.Secret
	err := n.client.Get(ctx, types.NamespacedName{
		Name:      userSecretName(email),
		Namespace: namespace,
	}, &secret)
	if errors.IsNotFound(err) {
		return fmt.Errorf("invalid credentials")
	}
	if err != nil {
		return fmt.Errorf("reading user secret: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword(secret.Data["password_hash"], []byte(password)); err != nil {
		return fmt.Errorf("invalid credentials")
	}
	return nil
}

func principalFromSecret(secret *corev1.Secret) Principal {
	return Principal{
		ID:          string(secret.Data["email"]),
		Email:       string(secret.Data["email"]),
		Role:        Role(secret.Data["role"]),
		PasswordGen: passwordGenFromSecret(secret),
	}
}

func passwordGenFromSecret(secret *corev1.Secret) int64 {
	var gen int64
	if raw, ok := secret.Data["password_gen"]; ok {
		fmt.Sscanf(string(raw), "%d", &gen)
	}
	return gen
}
