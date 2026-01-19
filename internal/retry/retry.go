package retry

import (
	"context"
	"io"
	"math/rand"
	"net/http"
	"time"

	"modern_reverse_proxy/internal/policy"
)

type AttemptFunc func(ctx context.Context) (*http.Response, error, string)

type Budgets struct {
	Route  *Budget
	Client *Budget
}

type Config struct {
	Policy        policy.RetryPolicy
	OuterContext  context.Context
	AllowRetry    bool
	BudgetEnabled bool
	BudgetError   bool
	Budgets       Budgets
	OnRetry       func(reason string)
}

type Result struct {
	Response             *http.Response
	Err                  error
	RetryCount           int
	RetryReason          string
	RetryBudgetExhausted bool
	UpstreamAddr         string
}

func Execute(cfg Config, attempt AttemptFunc) Result {
	result := Result{}
	if attempt == nil {
		result.Err = context.Canceled
		return result
	}

	policyConfig := cfg.Policy
	maxAttempts := policyConfig.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if !policyConfig.Enabled || !cfg.AllowRetry {
		maxAttempts = 1
	}

	outerCtx := cfg.OuterContext
	if outerCtx == nil {
		outerCtx = context.Background()
	}
	deadline, hasDeadline := outerCtx.Deadline()
	start := time.Now()

	attempts := 0
	for attempts < maxAttempts {
		attempts++

		remainingOuter := time.Duration(1<<63 - 1)
		if hasDeadline {
			remainingOuter = time.Until(deadline)
		}
		remainingBudget := remainingOuter
		if policyConfig.TotalRetryBudget > 0 {
			remainingBudget = policyConfig.TotalRetryBudget - time.Since(start)
		}
		perTry := computePerTryTimeout(policyConfig.PerTryTimeout, remainingOuter, remainingBudget)
		if perTry <= 0 {
			result.Err = context.DeadlineExceeded
			return result
		}

		attemptCtx, cancel := context.WithTimeout(outerCtx, perTry)
		resp, err, upstreamAddr := attempt(attemptCtx)
		cancel()
		result.UpstreamAddr = upstreamAddr

		if err == nil {
			reason, retryable := ClassifyStatus(resp.StatusCode, policyConfig)
			if retryable && attempts < maxAttempts && cfg.AllowRetry {
				result.RetryReason = reason
				if !consumeBudgets(&result, cfg) {
					return resultWithResponse(result, resp)
				}
				result.RetryCount++
				if cfg.OnRetry != nil {
					cfg.OnRetry(reason)
				}
				drainResponse(resp)
				if !sleepWithBackoff(outerCtx, policyConfig.Backoff, policyConfig.BackoffJitter) {
					result.Err = outerCtx.Err()
					return result
				}
				continue
			}
			result.Response = resp
			return result
		}

		if outerCtx.Err() != nil {
			result.Err = outerCtx.Err()
			return result
		}

		reason, retryable := ClassifyError(err)
		if !retryable || attempts >= maxAttempts || !cfg.AllowRetry {
			result.Err = err
			return result
		}
		if policyConfig.RetryOnErrors == nil || !policyConfig.RetryOnErrors[reason] {
			result.Err = err
			return result
		}
		result.RetryReason = reason
		if !consumeBudgets(&result, cfg) {
			result.Err = err
			return result
		}
		result.RetryCount++
		if cfg.OnRetry != nil {
			cfg.OnRetry(reason)
		}
		if !sleepWithBackoff(outerCtx, policyConfig.Backoff, policyConfig.BackoffJitter) {
			result.Err = outerCtx.Err()
			return result
		}
	}

	if result.Err == nil && result.Response == nil {
		result.Err = context.DeadlineExceeded
	}
	return result
}

func computePerTryTimeout(perTry time.Duration, remainingOuter time.Duration, remainingBudget time.Duration) time.Duration {
	if remainingOuter <= 0 || remainingBudget <= 0 {
		return 0
	}
	if perTry <= 0 {
		if remainingOuter < remainingBudget {
			return remainingOuter
		}
		return remainingBudget
	}
	if remainingOuter < perTry {
		perTry = remainingOuter
	}
	if remainingBudget < perTry {
		perTry = remainingBudget
	}
	return perTry
}

func consumeBudgets(result *Result, cfg Config) bool {
	if !cfg.BudgetEnabled {
		return true
	}
	if cfg.BudgetError {
		result.RetryBudgetExhausted = true
		return false
	}
	if cfg.Budgets.Route != nil && !cfg.Budgets.Route.Consume() {
		result.RetryBudgetExhausted = true
		return false
	}
	if cfg.Budgets.Client != nil && !cfg.Budgets.Client.Consume() {
		result.RetryBudgetExhausted = true
		return false
	}
	return true
}

func drainResponse(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func sleepWithBackoff(ctx context.Context, backoff time.Duration, jitter time.Duration) bool {
	delay := backoff
	if jitter > 0 {
		jitterValue := time.Duration(rand.Int63n(int64(jitter) + 1))
		delay += jitterValue
	}
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func resultWithResponse(result Result, resp *http.Response) Result {
	result.Response = resp
	return result
}
