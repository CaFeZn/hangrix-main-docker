// Package service holds the business-logic layer for the llm_provider module.
// This file contains the pure-function exponential backoff calculator used
// by the group router's state machine.
package service

import "time"

const (
	// backoffBase is the starting backoff duration (1 minute).
	backoffBase = 60 * time.Second
	// backoffCap is the maximum backoff duration (7 days).
	backoffCap = 7 * 24 * time.Hour
)

// NextBackoff computes the next exponential backoff step and the wall-clock
// deadline until which the member should remain auto-disabled.
//
// The formula is: duration = min(cap, base << step), where base is 60 seconds.
// Step 0 → 1 min, step 1 → 2 min, step 2 → 4 min, … cap 7 days.
//
// The returned newStep is always step+1; callers store it regardless of whether
// the cap was hit, so subsequent failures keep the member at the cap.
func NextBackoff(step int32) (newStep int32, until time.Time) {
	newStep = step + 1
	d := backoffBase
	if step > 0 {
		// Guard against overflow: if the shift would exceed the cap or overflow int64,
		// just use the cap directly. backoffBase<<19 ≈ 3.6 days; backoffBase<<20 ≈ 7.3d.
		// Shift past 30 overflows int64; cap before it ever gets there.
		if step < 20 && backoffBase<<step <= backoffCap {
			d = backoffBase << step
		} else {
			d = backoffCap
		}
	}
	return newStep, time.Now().UTC().Add(d)
}

// isRetryableFailure classifies an HTTP status code for group failover.
// 5xx, 429 (rate-limit), and specific 4xx codes (408/425) trigger failover
// and backoff increment. Client errors (400/401/403/404/422) are not retried
// because they indicate a request problem rather than an upstream health issue.
//
// Note: 429 is currently treated identically to 5xx for backoff purposes.
// A future improvement could use a shorter initial backoff for rate-limit
// responses, which typically clear within 1 minute.
func isRetryableFailure(statusCode int) bool {
	switch statusCode {
	case 408, 425, 429, 500, 502, 503, 504, 529:
		return true
	default:
		return statusCode >= 500
	}
}
