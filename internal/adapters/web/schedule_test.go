package web

import (
	"net/url"
	"testing"
)

func TestScheduleBuilder(t *testing.T) {
	for _, tc := range []struct {
		name string
		form url.Values
		want string
	}{
		{"manual", url.Values{"schedule_mode": {"manual"}, "cron": {"* * * * *"}}, ""},
		{"minutes", url.Values{"schedule_mode": {"minutes"}, "schedule_interval": {"15"}}, "*/15 * * * *"},
		{"hourly", url.Values{"schedule_mode": {"hourly"}, "schedule_minute": {"7"}}, "7 * * * *"},
		{"daily", url.Values{"schedule_mode": {"daily"}, "schedule_time": {"09:05"}}, "5 9 * * *"},
		{"weekly", url.Values{"schedule_mode": {"weekly"}, "schedule_time": {"16:30"}, "schedule_days": {"5", "1", "1"}}, "30 16 * * 1,5"},
		{"monthly", url.Values{"schedule_mode": {"monthly"}, "schedule_time": {"00:00"}, "schedule_month_day": {"31"}}, "0 0 31 * *"},
		{"advanced", url.Values{"schedule_mode": {"custom"}, "cron": {" 10 */5 * * * * "}}, "10 */5 * * * *"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildSchedule(tc.form)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, values := range []url.Values{
		{"schedule_mode": {"minutes"}, "schedule_interval": {"0"}},
		{"schedule_mode": {"minutes"}, "schedule_interval": {"7"}},
		{"schedule_mode": {"hourly"}, "schedule_minute": {"60"}},
		{"schedule_mode": {"daily"}, "schedule_time": {"24:00"}},
		{"schedule_mode": {"daily"}, "schedule_time": {"bad"}},
		{"schedule_mode": {"weekly"}, "schedule_time": {"09:00"}},
		{"schedule_mode": {"weekly"}, "schedule_time": {"09:00"}, "schedule_days": {"7"}},
		{"schedule_mode": {"monthly"}, "schedule_time": {"09:00"}, "schedule_month_day": {"0"}},
		{"schedule_mode": {"unknown"}},
	} {
		if _, err := buildSchedule(values); err == nil {
			t.Fatalf("accepted invalid schedule: %v", values)
		}
	}
}

func TestExistingSchedulesRoundTrip(t *testing.T) {
	for _, spec := range []string{"", "*/15 * * * *", "0 0 * * *", "7 * * * *", "30 16 * * 1,5", "0 0 31 * *", "10 */5 * * * *", "0 0 * * MON-FRI", "0 0 1 * 1", "*/7 * * * *", "0 00 * * *"} {
		f := readSchedule(spec)
		values := url.Values{"schedule_mode": {f.Mode}, "schedule_time": {f.Time}, "schedule_minute": {f.Minute}, "schedule_interval": {f.Interval}, "schedule_month_day": {f.MonthDay}, "schedule_days": f.Days, "cron": {f.Cron}}
		got, err := buildSchedule(values)
		if err != nil || got != spec {
			t.Fatalf("schedule %q changed to %q: %v", spec, got, err)
		}
	}
	zones := timezoneChoices("US/Eastern", "Europe/Madrid")
	selected, madrid, utc := 0, false, false
	for _, zone := range zones {
		if zone.Selected {
			selected++
			if zone.Value != "US/Eastern" {
				t.Fatal("saved timezone alias lost")
			}
		}
		madrid = madrid || zone.Value == "Europe/Madrid"
		utc = utc || zone.Value == "UTC"
	}
	if selected != 1 || !madrid || !utc {
		t.Fatal("incomplete timezone choices")
	}
}
