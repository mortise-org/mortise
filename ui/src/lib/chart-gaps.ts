// Pure math for the observer's gap-visibility contract: coverage is
// [bucketTs, 0|1] per step bucket, where 0 means the bucket was NOT
// observed. These functions are the no-interpolation guarantee itself —
// UtilizationChart renders whatever they return, so they are unit-tested
// directly (chart-gaps.test.ts) without a DOM.

export type SeriesPoint = [number, number];
export type Coverage = [number, number][];

// The bucket width comes from the coverage array's own spacing; a
// single-bucket array can't reveal it, so fall back to 60s.
export function coverageStepOf(coverage: Coverage): number {
	return coverage.length > 1 ? coverage[1][0] - coverage[0][0] : 60;
}

export function unobservedBuckets(coverage: Coverage): Set<number> {
	const set = new Set<number>();
	for (const [ts, observed] of coverage) if (observed === 0) set.add(ts);
	return set;
}

// Consecutive 0-buckets merge into one [start, end) range.
export function gapRanges(coverage: Coverage): Array<{ start: number; end: number }> {
	const out: Array<{ start: number; end: number }> = [];
	if (coverage.length < 1) return out;
	const step = coverageStepOf(coverage);
	for (const [ts, observed] of coverage) {
		if (observed === 1) continue;
		const last = out[out.length - 1];
		if (last && ts === last.end) last.end = ts + step;
		else out.push({ start: ts, end: ts + step });
	}
	return out;
}

// splitAtGaps splits a series at unobserved buckets so no line is ever
// drawn across a gap:
//   - a point falling inside an unobserved bucket is DROPPED — deliberate:
//     the coverage signal wins over a stray point, since a point the
//     observer says it didn't collect is not evidence;
//   - any unobserved bucket strictly BETWEEN two points also breaks the
//     segment, so a gap narrower than the point spacing still shows;
//   - single-point segments are returned as length-1 arrays (rendered as
//     dots, since a one-point path is invisible).
export function splitAtGaps(
	points: SeriesPoint[],
	unobserved: Set<number>,
	step: number
): SeriesPoint[][] {
	const sorted = [...points].sort((a, b) => a[0] - b[0]);
	const segs: SeriesPoint[][] = [];
	let cur: SeriesPoint[] = [];
	for (const p of sorted) {
		const bucket = step > 0 ? Math.floor(p[0] / step) * step : p[0];
		const inGap = unobserved.has(bucket);
		if (cur.length > 0) {
			const prev = cur[cur.length - 1][0];
			let broken = inGap;
			if (!broken && step > 0) {
				for (let b = Math.floor(prev / step) * step + step; b < p[0]; b += step) {
					if (unobserved.has(b)) {
						broken = true;
						break;
					}
				}
			}
			if (broken) {
				segs.push(cur);
				cur = [];
			}
		}
		if (!inGap) cur.push(p);
	}
	if (cur.length > 0) segs.push(cur);
	return segs;
}
