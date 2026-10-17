import { describe, expect, test } from 'bun:test';
import {
  channelStatusRate,
  channelStatusTone,
  formatChannelRate,
  latestNonEmptyBucketIndex,
} from '../src/features/channelStatus/utils';

describe('channel status thresholds', () => {
  test('maps success rates to the screenshot tones', () => {
    expect(channelStatusTone(85, 15)).toBe('good');
    expect(channelStatusTone(100, 0)).toBe('good');
    expect(channelStatusTone(84, 16)).toBe('warning');
    expect(channelStatusTone(50, 50)).toBe('warning');
    expect(channelStatusTone(49, 51)).toBe('critical');
    expect(channelStatusTone(0, 0)).toBe('idle');
  });

  test('formats rates and tolerates empty buckets', () => {
    expect(channelStatusRate(1, 3)).toBe(0.25);
    expect(formatChannelRate(1, 3)).toBe('25.00%');
    expect(formatChannelRate(0, 0)).toBe('--');
  });

  test('selects the latest non-empty bucket', () => {
    const buckets = [
      { start: '2026-10-17T00:00:00Z', success: 0, failed: 0 },
      { start: '2026-10-17T00:20:00Z', success: 1, failed: 0 },
      { start: '2026-10-17T00:40:00Z', success: 0, failed: 0 },
    ];
    expect(latestNonEmptyBucketIndex(buckets)).toBe(1);
    expect(latestNonEmptyBucketIndex([{ start: 'x', success: 0, failed: 0 }])).toBe(-1);
  });
});
