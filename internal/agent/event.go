package agent

import (
	"time"

	"charm.land/fantasy"
)

// eventPromptSent is a no-op; telemetry has been removed.
func (a *sessionAgent) eventPromptSent(sessionID string) {}

// eventPromptResponded is a no-op; telemetry has been removed.
func (a *sessionAgent) eventPromptResponded(sessionID string, duration time.Duration) {}

// eventTokensUsed is a no-op; telemetry has been removed.
func (a *sessionAgent) eventTokensUsed(sessionID string, model Model, usage fantasy.Usage, cost float64) {
}
