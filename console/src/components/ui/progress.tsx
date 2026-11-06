import type { FC } from 'hono/jsx';

export interface Segment {
  label: string;
  value: number;
  tone: 'ink' | 'pink' | 'gray';
}

// SegmentBar draws one bar split into parts. Each part has a legend entry.
export const SegmentBar: FC<{ segments: Segment[]; label: string }> = ({ segments, label }) => {
  const total = segments.reduce((sum, segment) => sum + segment.value, 0);

  return (
    <div class="segbar" role="img" aria-label={label}>
      {segments
        .filter(segment => segment.value > 0 && total > 0)
        .map(segment => (
          <span class={`seg ${segment.tone}`} style={`width:${((segment.value / total) * 100).toFixed(2)}%`} />
        ))}
    </div>
  );
};
