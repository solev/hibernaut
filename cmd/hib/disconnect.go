package main

import (
	"context"
	"errors"
	"slices"

	"github.com/solev/hibernaut/internal/hibapi"
)

func disconnect(provider string) runner {
	return configured(func(a *app, ctx context.Context, cfg *config, _ []string) int {
		features, err := a.features(ctx, cfg)
		if err != nil {
			return a.failWaiting(ctx, err, "")
		}
		if !slices.Contains(features, hibapi.FeatureDisconnect) {
			return a.fail(errors.New("the host's hibd is too old for disconnect: run make up to update it"))
		}
		if _, err := a.remoteSudo(ctx, cfg, shortTimeout, callOptions{}, "disconnect", provider, "--json"); err != nil {
			return a.failWaiting(ctx, err, "The host may still remove the login: check hib status")
		}
		return 0
	})
}
