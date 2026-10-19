package web

import (
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

//go:embed timezones.txt
var timezoneNames string

type scheduleForm struct {
	Mode, Time, Minute, Interval, MonthDay, Cron string
	Days                                         []string
}

func (f scheduleForm) UsesTimezone() bool {
	switch f.Mode {
	case "manual", "minutes", "hourly":
		return false
	default:
		return true
	}
}

type choice struct {
	Value, Label string
	Selected     bool
}

func timezoneChoices(current, fallback string) []choice {
	zones := strings.Fields(timezoneNames)
	zones = append(zones, current, fallback)
	sort.Strings(zones)
	result := []choice{}
	previous := ""
	for _, zone := range zones {
		if zone == "" || zone == previous {
			continue
		}
		result = append(result, choice{zone, zone, zone == current})
		previous = zone
	}
	return result
}

func readSchedule(spec string) scheduleForm {
	f := scheduleForm{Mode: "custom", Time: "00:00", Minute: "0", Interval: "15", MonthDay: "1", Cron: spec}
	if spec == "" {
		f.Mode = "manual"
		return f
	}
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return f
	}
	minute, mErr := strconv.Atoi(fields[0])
	hour, hErr := strconv.Atoi(fields[1])
	if mErr == nil && hErr == nil {
		f.Time = fmt.Sprintf("%02d:%02d", hour, minute)
	}
	if fields[2] == "*" && fields[3] == "*" && fields[4] == "*" {
		switch {
		case fields[1] == "*" && strings.HasPrefix(fields[0], "*/"):
			f.Mode, f.Interval = "minutes", strings.TrimPrefix(fields[0], "*/")
		case fields[1] == "*" && fields[0] == "*":
			f.Mode, f.Interval = "minutes", "1"
		case fields[1] == "*" && mErr == nil:
			f.Mode, f.Minute = "hourly", strconv.Itoa(minute)
		case mErr == nil && hErr == nil:
			f.Mode = "daily"
		}
	} else if mErr == nil && hErr == nil && fields[3] == "*" {
		if fields[2] == "*" && fields[4] != "*" {
			days := strings.Split(fields[4], ",")
			valid := true
			for _, day := range days {
				n, err := strconv.Atoi(day)
				valid = valid && err == nil && n >= 0 && n <= 6
			}
			if valid {
				f.Mode, f.Days = "weekly", days
			}
		} else if day, err := strconv.Atoi(fields[2]); err == nil && day >= 1 && day <= 31 && fields[4] == "*" {
			f.Mode, f.MonthDay = "monthly", strconv.Itoa(day)
		}
	}
	// Only select the builder when it reproduces the original schedule exactly.
	values := url.Values{"schedule_mode": {f.Mode}, "schedule_time": {f.Time}, "schedule_minute": {f.Minute}, "schedule_interval": {f.Interval}, "schedule_month_day": {f.MonthDay}, "schedule_days": f.Days, "cron": {spec}}
	if built, err := buildSchedule(values); err != nil || built != spec {
		f.Mode = "custom"
	}
	return f
}

func submittedSchedule(values url.Values) scheduleForm {
	return scheduleForm{Mode: values.Get("schedule_mode"), Time: values.Get("schedule_time"), Minute: values.Get("schedule_minute"), Interval: values.Get("schedule_interval"), MonthDay: values.Get("schedule_month_day"), Days: values["schedule_days"], Cron: values.Get("cron")}
}

func buildSchedule(values url.Values) (string, error) {
	f := submittedSchedule(values)
	number := func(value string, min, max int) (int, error) {
		n, err := strconv.Atoi(value)
		if err != nil || n < min || n > max {
			return 0, errors.New("choose a valid schedule value")
		}
		return n, nil
	}
	switch f.Mode {
	case "manual":
		return "", nil
	case "", "custom":
		return strings.TrimSpace(f.Cron), nil
	case "minutes":
		n, err := number(f.Interval, 1, 30)
		if err != nil || 60%n != 0 {
			return "", errors.New("choose a minute interval that divides evenly into an hour")
		}
		return fmt.Sprintf("*/%d * * * *", n), nil
	case "hourly":
		n, err := number(f.Minute, 0, 59)
		return fmt.Sprintf("%d * * * *", n), err
	case "daily", "weekly", "monthly":
		parts := strings.Split(f.Time, ":")
		if len(parts) != 2 {
			return "", errors.New("choose a time in HH:MM format")
		}
		hour, hErr := number(parts[0], 0, 23)
		minute, mErr := number(parts[1], 0, 59)
		if hErr != nil || mErr != nil {
			return "", errors.New("choose a valid time")
		}
		day, weekdays := "*", "*"
		if f.Mode == "monthly" {
			n, err := number(f.MonthDay, 1, 31)
			if err != nil {
				return "", err
			}
			day = strconv.Itoa(n)
		}
		if f.Mode == "weekly" {
			if len(f.Days) == 0 {
				return "", errors.New("choose at least one weekday")
			}
			selected := map[int]bool{}
			for _, value := range f.Days {
				n, err := number(value, 0, 6)
				if err != nil {
					return "", err
				}
				selected[n] = true
			}
			days := []string{}
			for n := 0; n < 7; n++ {
				if selected[n] {
					days = append(days, strconv.Itoa(n))
				}
			}
			weekdays = strings.Join(days, ",")
		}
		return fmt.Sprintf("%d %d %s * %s", minute, hour, day, weekdays), nil
	default:
		return "", errors.New("choose a schedule frequency")
	}
}

func weekdayChoices(days []string) []choice {
	result := []choice{}
	for n, name := range []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"} {
		value, selected := strconv.Itoa(n), false
		for _, day := range days {
			selected = selected || day == value
		}
		result = append(result, choice{value, name, selected})
	}
	return result
}

func scheduleDescription(spec string) string {
	f := readSchedule(spec)
	switch f.Mode {
	case "manual":
		return "Manual only"
	case "minutes":
		return "Every " + f.Interval + " minutes"
	case "hourly":
		return "Every hour at minute " + f.Minute
	case "daily":
		return "Every day at " + f.Time
	case "weekly":
		names := []string{}
		for _, day := range weekdayChoices(f.Days) {
			if day.Selected {
				names = append(names, day.Label)
			}
		}
		return strings.Join(names, ", ") + " at " + f.Time
	case "monthly":
		return "Day " + f.MonthDay + " of each month at " + f.Time
	default:
		return "Custom: " + spec
	}
}
