package event_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/event"
)

func TestIsValidationNumber(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"12", true},
		// source: spec/services/events/post_validation_service_spec.rb:41
		{"-12", true},
		{"12.50", true},
		{"0.0001", true},
		{"", false},
		{"+12", false},
		{".5", false},
		{"5.", false},
		{"1e3", false},
		{"1,5", false},
		{" 12", false},
		{"abc", false},
	} {
		if got := event.IsValidationNumber(tc.in); got != tc.want {
			t.Errorf("IsValidationNumber(%q) got = %v, want = %v", tc.in, got, tc.want)
		}
	}
}

func TestValidationWindow(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		at       time.Time
		from, to time.Time
	}{
		{"mid-hour", time.Date(2026, 6, 1, 10, 5, 0, 0, time.UTC), time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC), time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)},
		{"on the hour", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 31, 23, 0, 0, 0, time.UTC), time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
		{"other zone", time.Date(2026, 6, 1, 12, 30, 0, 0, paris), time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC), time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to := event.ValidationWindow(tc.at)
			if !from.Equal(tc.from) || !to.Equal(tc.to) || from.Location() != time.UTC {
				t.Errorf("ValidationWindow() got = [%v, %v), want = [%v, %v) in UTC", from, to, tc.from, tc.to)
			}
		})
	}
}

func TestValidationErrorsNormalize(t *testing.T) {
	v := event.ValidationErrors{InvalidCode: []string{"b", "a", "b"}}
	v.Normalize()
	want := event.ValidationErrors{InvalidCode: []string{"a", "b"}, MissingAggregationProperty: []string{}, InvalidFilterValues: []string{}}
	if !reflect.DeepEqual(v, want) {
		t.Errorf("Normalize() got = %+v, want = %+v", v, want)
	}
	if v.Empty() {
		t.Errorf("Empty() got = true, want = false")
	}
}
