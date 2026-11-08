package decoder

import (
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// TestTimestampText pins Postgres's timestamp rendering on the cases the
// dump has none of; TestProjectionPostgres covers the rest.
func TestTimestampText(t *testing.T) {
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2015, 4, 21, 11, 37, 26, 607_000_000, time.UTC), "2015-04-21 11:37:26.607"},
		{time.Date(2019, 12, 21, 20, 26, 30, 0, time.UTC), "2019-12-21 20:26:30"},
		{time.Date(2020, 1, 2, 3, 4, 5, 120_000, time.UTC), "2020-01-02 03:04:05.00012"},
		{time.Date(2020, 1, 2, 3, 4, 5, 999_999_000, time.UTC), "2020-01-02 03:04:05.999999"},
		{time.Date(5, 6, 7, 8, 9, 10, 0, time.UTC), "0005-06-07 08:09:10"},
		{time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), "0001-01-01 00:00:00 BC"},
		{time.Date(12345, 1, 1, 0, 0, 0, 0, time.UTC), "12345-01-01 00:00:00"},
	} {
		if got := timestampText(c.t.UnixMicro()); got != contract.Value(c.want) {
			t.Errorf("%v: %+v, want %q", c.t, got, c.want)
		}
	}
}
