package websearch

import (
	"testing"
	"time"
)

func TestQuotaResetDeadline(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		channel Channel
		quota   *QuotaSnapshot
		want    time.Time
	}{
		{
			name:    "upstream reset wins",
			channel: ChannelFirecrawl,
			quota:   &QuotaSnapshot{ResetAt: timePointer(time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC))},
			want:    time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC),
		},
		{
			name:    "daily fallback",
			channel: ChannelTinyfish,
			want:    time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "monthly fallback",
			channel: ChannelTavily,
			want:    time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "stale upstream reset falls back",
			channel: ChannelFirecrawl,
			quota:   &QuotaSnapshot{ResetAt: timePointer(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))},
			want:    time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := QuotaResetDeadline(tc.channel, tc.quota, now); !got.Equal(tc.want) {
				t.Fatalf("deadline = %s，期望 %s", got, tc.want)
			}
		})
	}
}
