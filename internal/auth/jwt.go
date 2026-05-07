package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	jwtSecretName = "mortise-jwt-key"
	jwtSecretKey  = "signing-key"
	namespace     = "mortise-system"
	tokenExpiry   = 24 * time.Hour
	jwtIssuer     = "mortise"
	jwtAudience   = "mortise-api"
)

type JWTHelper struct {
	client client.Client
}

func NewJWTHelper(c client.Client) *JWTHelper {
	return &JWTHelper{client: c}
}

func (h *JWTHelper) signingKey(ctx context.Context) ([]byte, error) {
	var secret corev1.Secret
	err := h.client.Get(ctx, types.NamespacedName{Name: jwtSecretName, Namespace: namespace}, &secret)
	if err == nil {
		if key, ok := secret.Data[jwtSecretKey]; ok && len(key) > 0 {
			return key, nil
		}
	}
	if !errors.IsNotFound(err) && err != nil {
		return nil, fmt.Errorf("reading jwt key secret: %w", err)
	}

	// Auto-create signing key
	key := make([]byte, 64)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating signing key: %w", err)
	}

	secret = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jwtSecretName,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			jwtSecretKey: key,
		},
	}
	if err := h.client.Create(ctx, &secret); err != nil {
		if errors.IsAlreadyExists(err) {
			// Race: another instance created it; re-read
			return h.signingKey(ctx)
		}
		return nil, fmt.Errorf("creating jwt key secret: %w", err)
	}
	return key, nil
}

func (h *JWTHelper) GenerateToken(ctx context.Context, p Principal) (string, error) {
	key, err := h.signingKey(ctx)
	if err != nil {
		return "", err
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"sub":     p.ID,
		"email":   p.Email,
		"role":    string(p.Role),
		"pwd_gen": p.PasswordGen,
		"iss":     jwtIssuer,
		"aud":     jwtAudience,
		"iat":     now.Unix(),
		"exp":     now.Add(tokenExpiry).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(key)
}

func (h *JWTHelper) ValidateToken(ctx context.Context, tokenString string) (Principal, int64, error) {
	key, err := h.signingKey(ctx)
	if err != nil {
		return Principal{}, 0, err
	}

	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return key, nil
	}, jwt.WithIssuer(jwtIssuer), jwt.WithAudience(jwtAudience))
	if err != nil {
		return Principal{}, 0, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return Principal{}, 0, fmt.Errorf("invalid token claims")
	}

	sub, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)
	role, _ := claims["role"].(string)
	pwdGen, _ := claims["pwd_gen"].(float64)

	return Principal{
		ID:          sub,
		Email:       email,
		Role:        Role(role),
		PasswordGen: int64(pwdGen),
	}, int64(pwdGen), nil
}
