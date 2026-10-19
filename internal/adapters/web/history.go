package web

import (
	"fmt"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"strings"
	"time"
)

type runView struct {
	domain.Run
	Trigger, Duration, Description string
}

func executionView(run domain.Run) runView {
	v := runView{Run: run, Trigger: "Scheduled", Duration: "Not recorded"}
	if strings.HasPrefix(run.Slot, "manual:") {
		v.Trigger = "Manual"
	}
	if run.JobName == "" {
		v.JobName = run.Job
	}
	if run.DurationMS >= 0 {
		v.Duration = fmt.Sprintf("%d ms", run.DurationMS)
		if run.DurationMS >= 1000 {
			v.Duration = (time.Duration(run.DurationMS) * time.Millisecond).String()
		}
	} else {
		started, startErr := time.Parse("2006-01-02 15:04:05", run.Started)
		finished, endErr := time.Parse("2006-01-02 15:04:05", run.Finished)
		if startErr == nil && endErr == nil && !finished.Before(started) {
			v.Duration = finished.Sub(started).String() + " (approx.)"
		} else if startErr == nil && run.Status == "running" {
			v.Duration = time.Since(started).Truncate(time.Second).String() + " (running)"
		}
	}
	switch run.Status {
	case "running":
		v.Description = "Execution is in progress. Refresh activity to see the result."
	case "success":
		v.Description = "The task completed successfully."
	case "fallback":
		v.Description = "The task completed using fallback authentication."
	case "interrupted":
		v.Description = "The service stopped before a final result was recorded. The last request may have reached the endpoint. This execution will not resume automatically."
	case "failed":
		v.Description = "The task failed. Detailed errors were not recorded for this older run."
		switch run.ErrorCode {
		case "HTTP_ERROR":
			v.Description = "The endpoint returned an unsuccessful HTTP status."
		case "REQUEST_ABORTED":
			v.Description = "The request timed out or was canceled before completion."
		case "NETWORK_ERROR":
			v.Description = "The endpoint could not be reached or the connection failed."
			if run.HTTPStatus >= 300 && run.HTTPStatus < 400 {
				v.Description = "The endpoint returned a redirect. Redirects are not followed."
			}
		case "INVALID_JSON":
			v.Description = "The endpoint returned invalid JSON where this task requires a JSON response."
		case "response_too_large":
			v.Description = "The response exceeded the 1 MB size limit."
		case "request_failed":
			v.Description = "The request failed. Check the endpoint configuration and service availability."
		}
	}
	if run.FallbackUsed && run.Status == "fallback" {
		v.Description = "The primary request failed; the fallback endpoint completed successfully."
	}
	return v
}
