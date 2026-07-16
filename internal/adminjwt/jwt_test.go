package adminjwt_test

import (
	"context"
	"testing"
	"time"

	"github.com/cisco/rpc-node-gateway/internal/adminjwt"
)

func TestIssueAndValidate(t *testing.T) {
	store := adminjwt.NewMemoryStore()
	issuer := adminjwt.Issuer{
		Secret:  []byte("test-secret"),
		Issuer:  "rpc-node-gateway",
		Subject: "admin-stats",
		TTL:     time.Hour,
	}
	rec, err := issuer.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), rec); err != nil {
		t.Fatal(err)
	}

	v := adminjwt.Validator{
		Secret:  []byte("test-secret"),
		Issuer:  "rpc-node-gateway",
		Subject: "admin-stats",
		Store:   store,
	}
	if _, err := v.Validate(context.Background(), rec.Token); err != nil {
		t.Fatalf("validate: %v", err)
	}

	_ = store.RevokeAllActive(context.Background())
	if _, err := v.Validate(context.Background(), rec.Token); err == nil {
		t.Fatal("expected revoked token to fail")
	}
}

func TestRotator(t *testing.T) {
	store := adminjwt.NewMemoryStore()
	r := &adminjwt.Rotator{
		Issuer: adminjwt.Issuer{
			Secret:  []byte("test-secret"),
			Issuer:  "rpc-node-gateway",
			Subject: "admin-stats",
			TTL:     time.Hour,
		},
		Store: store,
		Cfg: adminjwt.RotatorConfig{
			RevokePrevious: true,
			LogToken:       false,
		},
	}
	if err := r.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	latest, err := store.LatestActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if latest.Token == "" {
		t.Fatal("empty token")
	}
}
