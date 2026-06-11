package upstream

import "errors"

// DispatchClass classifies a dispatch error to determine whether the proxy
// should fail over to the next candidate and what HTTP status to surface.
type DispatchClass struct {
	StatusCode int
	FailOver   bool // true = try the next candidate; false = stop and return
}

// ClassifyDispatchError inspects the error returned by Provider.Respond and
// returns a DispatchClass that tells the handler whether to retry.
//
// Classification rules:
//   - Upstream transient errors (408, 425, 429, 5xx, 529): fail over + backoff
//   - Upstream client errors (400, 401, 403, 404, 422): stop (request problem)
//   - Sentinel errors (ErrStreamingUnsupported, ErrBaseURLRequired): stop
//   - Network / decode errors (anything else): fail over
func ClassifyDispatchError(err error) DispatchClass {
	var ue *UpstreamError
	if errors.As(err, &ue) {
		sc := ue.StatusCode
		switch sc {
		case 408, 425, 429, 500, 502, 503, 504, 529:
			return DispatchClass{StatusCode: sc, FailOver: true}
		case 400, 401, 403, 404, 422:
			return DispatchClass{StatusCode: sc, FailOver: false}
		default:
			return DispatchClass{StatusCode: sc, FailOver: sc >= 500}
		}
	}
	if errors.Is(err, ErrStreamingUnsupported) || errors.Is(err, ErrBaseURLRequired) {
		return DispatchClass{StatusCode: 0, FailOver: false}
	}
	// Network error, decode failure, or any other unexpected error → fail over.
	return DispatchClass{StatusCode: 0, FailOver: true}
}
