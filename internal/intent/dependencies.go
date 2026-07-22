package intent

import (
	"context"
	"errors"
	"fmt"
)

// ReadyDependents returns unpromoted versions whose declared dependencies are
// complete and whose direct dependency produced the current accepted intent.
func (repository *Repository) ReadyDependents(ctx context.Context) ([]Proposed, error) {
	current, found, err := repository.intents.CurrentIntent(ctx)
	if err != nil {
		return nil, fmt.Errorf("read current intent: %w", err)
	}
	if !found {
		return nil, errors.New("intent ledger is not initialized")
	}
	currentPromotion, found, err := repository.promotions.CompletedPromotionByIntent(ctx, current.ID)
	if err != nil {
		return nil, fmt.Errorf("read promotion for current intent: %w", err)
	}
	if !found {
		return nil, nil
	}
	versions, err := repository.changes.Dependents(ctx, currentPromotion.Promotion.VersionID)
	if err != nil {
		return nil, fmt.Errorf("read current version dependents: %w", err)
	}
	ready := make([]Proposed, 0, len(versions))
	for _, version := range versions {
		if _, promoted, err := repository.promotions.CompletedPromotion(ctx, version.ID); err != nil {
			return nil, fmt.Errorf("read dependent promotion: %w", err)
		} else if promoted {
			continue
		}
		dependenciesReady := true
		for _, dependencyID := range version.Dependencies {
			if _, promoted, err := repository.promotions.CompletedPromotion(ctx, dependencyID); err != nil {
				return nil, fmt.Errorf("read dependent dependency promotion: %w", err)
			} else if !promoted {
				dependenciesReady = false
				break
			}
		}
		if !dependenciesReady {
			continue
		}
		change, found, err := repository.changes.Change(ctx, version.ChangeID)
		if err != nil {
			return nil, fmt.Errorf("read dependent change: %w", err)
		}
		if !found {
			return nil, errors.New("dependent version change is not recorded")
		}
		ready = append(ready, Proposed{Change: change, Version: version})
	}
	return ready, nil
}
