package kusto

import (
	"fmt"
	"time"
)

const (
	// DefaultTimeout is the query budget when neither -timeout nor
	// query_timeout is set. It matches the client cutoff jacques always had;
	// the difference is that Kusto's servertimeout now equals it, so the
	// cluster abandons the query when jacques stops waiting instead of
	// running it for up to an hour with nobody listening.
	DefaultTimeout = 5 * time.Minute

	// MaxTimeout is Kusto's ceiling for the servertimeout request property.
	MaxTimeout = time.Hour

	// raiseHint names every knob that sets the budget.
	raiseHint = "raise it with -timeout <dur> or query_timeout in ~/.jacques/config.hcl (max 1h)"
)

// clientGrace is how long past servertimeout the HTTP client keeps waiting,
// so Kusto's own timeout error reaches us before ours fires. A var for tests.
var clientGrace = 30 * time.Second

// CheckTimeout validates a query budget. Values above Kusto's 1h maximum are
// refused rather than clamped, so a caller never gets less time than asked
// for without being told.
func CheckTimeout(d time.Duration) error {
	switch {
	case d <= 0:
		return fmt.Errorf("query timeout %s must be positive", d)
	case d > MaxTimeout:
		return fmt.Errorf("query timeout %s exceeds Kusto's maximum servertimeout of 1h; use -timeout 1h or less", d)
	}
	return nil
}

// formatTimespan renders d as a Kusto timespan literal (hh:mm:ss[.fff]).
func formatTimespan(d time.Duration) string {
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	d -= s * time.Second
	out := fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	if ms := d / time.Millisecond; ms > 0 {
		out += fmt.Sprintf(".%03d", ms)
	}
	return out
}

// serverTimeoutCode is the OneApiError code Kusto reports when a query
// exceeds its servertimeout (KustoServiceTimeoutException, "Query timed out").
const serverTimeoutCode = "RequestExecutionTimeout"

func serverTimeoutError(budget time.Duration, errs []oneAPIError) error {
	return fmt.Errorf("Kusto servertimeout of %s exceeded (%s); %s", budget, describeErrors(errs), raiseHint)
}
