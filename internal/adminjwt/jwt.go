package adminjwt

import (
	"context"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type Claims struct {
	jwt.RegisteredClaims
}

type Issuer struct {
	Secret  []byte
	Issuer  string
	Subject string
	TTL     time.Duration
}

func (i Issuer) Issue() (Record, error) {
	if len(i.Secret) == 0 {
		return Record{}, fmt.Errorf("jwt secret is empty")
	}
	if i.TTL <= 0 {
		return Record{}, fmt.Errorf("jwt ttl must be positive")
	}
	now := time.Now().UTC()
	jti := uuid.NewString()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Issuer:    i.Issuer,
			Subject:   i.Subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.TTL)),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(i.Secret)
	if err != nil {
		return Record{}, err
	}
	return Record{
		ID:        uuid.NewString(),
		JTI:       jti,
		Token:     signed,
		Subject:   i.Subject,
		IssuedAt:  now,
		ExpiresAt: now.Add(i.TTL),
		Revoked:   false,
	}, nil
}

type Validator struct {
	Secret  []byte
	Issuer  string
	Subject string
	Store   Store
}

func (v Validator) Validate(ctx context.Context, raw string) (*Claims, error) {
	if raw == "" {
		return nil, fmt.Errorf("empty token")
	}
	parsed, err := jwt.ParseWithClaims(raw, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return v.Secret, nil
	}, jwt.WithIssuer(v.Issuer), jwt.WithSubject(v.Subject))
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	if claims.ID == "" {
		return nil, fmt.Errorf("missing jti")
	}
	active, err := v.Store.IsActive(ctx, claims.ID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, fmt.Errorf("token revoked or not found")
	}
	return claims, nil
}
