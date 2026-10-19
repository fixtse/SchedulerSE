package application

import (
	"context"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"time"
)

// Only explicit HTTP failures are eligible by default. Timeouts, broken connections,
// redirects and unreadable success responses may follow a completed POST.
func recoveryAllowed(err error, uncertain bool, retry bool) bool {
	var failure *domain.Failure
	if !errors.As(err, &failure) || failure.Code != "HTTP_ERROR" {
		return uncertain
	}
	if retry {
		return failure.Status == 429 || failure.Status >= 500
	}
	return failure.Status >= 400
}

func (r *Runner) request(ctx context.Context, j domain.Job, slot string, timeout time.Duration, outcome *domain.Outcome) (domain.Response, error, error) {
	post := func(job domain.Job, fallback bool) (domain.Response, error, error) {
		if err := ctx.Err(); err != nil {
			return domain.Response{}, err, nil
		}
		if err := r.Store.Attempt(j.ID, slot, fallback); err != nil {
			return domain.Response{}, nil, err
		}
		outcome.Attempts++
		outcome.FallbackUsed = fallback
		requestCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		response, err := r.Sync.Post(requestCtx, job)
		return response, err, nil
	}
	response, requestErr, journalErr := post(j, false)
	for retry := 0; journalErr == nil && requestErr != nil && ctx.Err() == nil && retry < j.RetryCount && recoveryAllowed(requestErr, j.RetryUncertain, true); retry++ {
		delay := time.Duration(j.RetryDelayMS) * time.Millisecond
		if delay == 0 {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return response, ctx.Err(), nil
		case <-timer.C:
		}
		response, requestErr, journalErr = post(j, false)
	}
	if journalErr == nil && requestErr != nil && ctx.Err() == nil && j.FallbackURL != "" && recoveryAllowed(requestErr, j.RetryUncertain, false) {
		fallback := j
		fallback.URL, fallback.Authorization = j.FallbackURL, j.FallbackAuth
		// The alternate endpoint may return any successful response; it need not use
		// the legacy primary endpoint's authentication-response schema.
		fallback.Policy = "generic"
		response, requestErr, journalErr = post(fallback, true)
	}
	return response, requestErr, journalErr
}
