package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/templates"
	"go.uber.org/zap"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	provider, err := provider.NewProvider()
	if err != nil {
		log.Fatalf("failed to create provider: %v", err)
		return
	}
	u, err := templates.NewUbuntuServer24(ctx, provider)
	if err != nil {
		provider.Logger().Fatal("failed to create Ubuntu24", zap.Error(err))
		return
	}
	err = u.Start(ctx)
	if err != nil {
		provider.Logger().Fatal("failed to start Ubuntu24", zap.Error(err))
	}
}
