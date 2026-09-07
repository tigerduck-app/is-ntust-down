package probe

import (
	"context"
	"encoding/json"
	"time"
)

// QueryCourseProbe checks the public course query API that course search
// depends on. No authentication is involved, so it runs freely.
type QueryCourseProbe struct {
	URL       string
	UserAgent string
	Timeout   time.Duration
}

func (p *QueryCourseProbe) Key() string { return "querycourse.api" }

func (p *QueryCourseProbe) Run(ctx context.Context) Result {
	s, err := NewSession(p.Timeout, p.UserAgent)
	if err != nil {
		return down(0, 0, ReasonUnreachable)
	}
	started := time.Now()
	// The explicit JSON Accept header is required, not cosmetic: this API
	// performs strict content negotiation and answers an HTML-preferring
	// Accept with HTTP 500 and an XML error body.
	page, err := s.GetJSON(ctx, p.URL)
	latency := int(time.Since(started).Milliseconds())
	if err != nil {
		return down(latency, 0, classifyTransport(err))
	}
	if page.Status != 200 {
		return down(latency, page.Status, classifyStatus(page.Status))
	}

	var semesters []struct {
		Semester string `json:"Semester"`
	}
	if err := json.Unmarshal([]byte(page.Body), &semesters); err != nil {
		return down(latency, page.Status, ReasonUnexpectedBody)
	}
	// An empty array is a 200 that carries no data — the endpoint answered,
	// but nothing downstream of it would work.
	if len(semesters) == 0 || semesters[0].Semester == "" {
		return down(latency, page.Status, ReasonUnexpectedBody)
	}
	return up(latency, page.Status)
}
