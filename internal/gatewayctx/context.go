package gatewayctx

import (
	"context"

	"github.com/cisco/rpc-node-gateway/internal/model"
)

type ctxKey int

const (
	tokenKey ctxKey = iota
	chainKey
	methodsKey
)

func WithToken(ctx context.Context, t *model.Token) context.Context {
	return context.WithValue(ctx, tokenKey, t)
}

func Token(ctx context.Context) *model.Token {
	t, _ := ctx.Value(tokenKey).(*model.Token)
	return t
}

func WithChainID(ctx context.Context, chainID string) context.Context {
	return context.WithValue(ctx, chainKey, chainID)
}

func ChainID(ctx context.Context) string {
	s, _ := ctx.Value(chainKey).(string)
	return s
}

func WithMethods(ctx context.Context, methods []string) context.Context {
	return context.WithValue(ctx, methodsKey, methods)
}

func Methods(ctx context.Context) []string {
	m, _ := ctx.Value(methodsKey).([]string)
	return m
}
