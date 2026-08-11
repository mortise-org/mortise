import { describe, expect, it } from 'vitest';
import {
	coverageStepOf,
	gapRanges,
	splitAtGaps,
	unobservedBuckets,
	type Coverage,
	type SeriesPoint
} from './chart-gaps';

// Buckets are 60s wide starting at t=0 in every case below unless stated.
const STEP = 60;

function cov(...entries: Array<[number, number]>): Coverage {
	return entries;
}

describe('coverageStepOf', () => {
	it('reads the step from the array spacing', () => {
		expect(coverageStepOf(cov([0, 1], [300, 1]))).toBe(300);
	});

	it('falls back to 60 when the spacing is unknowable', () => {
		expect(coverageStepOf(cov())).toBe(60);
		expect(coverageStepOf(cov([120, 0]))).toBe(60);
	});
});

describe('gapRanges', () => {
	const cases: Array<{
		name: string;
		coverage: Coverage;
		want: Array<{ start: number; end: number }>;
	}> = [
		{ name: 'empty coverage → no ranges', coverage: cov(), want: [] },
		{
			name: 'all observed → no ranges',
			coverage: cov([0, 1], [60, 1], [120, 1]),
			want: []
		},
		{
			name: 'single unobserved bucket → one bucket-wide range',
			coverage: cov([0, 1], [60, 0], [120, 1]),
			want: [{ start: 60, end: 120 }]
		},
		{
			name: 'consecutive unobserved buckets merge into one range',
			coverage: cov([0, 1], [60, 0], [120, 0], [180, 1]),
			want: [{ start: 60, end: 180 }]
		},
		{
			name: 'separated unobserved buckets stay separate ranges',
			coverage: cov([0, 0], [60, 1], [120, 0]),
			want: [
				{ start: 0, end: 60 },
				{ start: 120, end: 180 }
			]
		},
		{
			name: 'single-entry unobserved coverage uses the fallback step',
			coverage: cov([300, 0]),
			want: [{ start: 300, end: 360 }]
		}
	];

	for (const c of cases) {
		it(c.name, () => {
			expect(gapRanges(c.coverage)).toEqual(c.want);
		});
	}
});

describe('unobservedBuckets', () => {
	it('collects only the 0-buckets', () => {
		expect(unobservedBuckets(cov([0, 1], [60, 0], [120, 0]))).toEqual(new Set([60, 120]));
	});
});

describe('splitAtGaps', () => {
	const cases: Array<{
		name: string;
		points: SeriesPoint[];
		unobserved: number[];
		want: SeriesPoint[][];
	}> = [
		{
			name: 'no gaps → one segment with all points',
			points: [
				[0, 1],
				[60, 2],
				[120, 3]
			],
			unobserved: [],
			want: [
				[
					[0, 1],
					[60, 2],
					[120, 3]
				]
			]
		},
		{
			name: 'point inside an unobserved bucket is dropped and breaks the segment',
			points: [
				[0, 1],
				[60, 2],
				[120, 3]
			],
			unobserved: [60],
			want: [[[0, 1]], [[120, 3]]]
		},
		{
			name: 'unobserved bucket strictly between two points breaks the segment',
			points: [
				[0, 1],
				[120, 3]
			],
			unobserved: [60],
			want: [[[0, 1]], [[120, 3]]]
		},
		{
			name: 'gap narrower than the point spacing still breaks (multi-bucket span)',
			points: [
				[0, 1],
				[300, 5]
			],
			unobserved: [120],
			want: [[[0, 1]], [[300, 5]]]
		},
		{
			name: 'a point isolated by gaps on both sides becomes a single-point segment',
			points: [
				[0, 1],
				[120, 2],
				[240, 3]
			],
			unobserved: [60, 180],
			want: [[[0, 1]], [[120, 2]], [[240, 3]]]
		},
		{
			name: 'points are sorted by timestamp before splitting',
			points: [
				[120, 3],
				[0, 1],
				[60, 2]
			],
			unobserved: [],
			want: [
				[
					[0, 1],
					[60, 2],
					[120, 3]
				]
			]
		},
		{
			name: 'a point mid-bucket floors to its bucket when testing observation',
			points: [
				[0, 1],
				[75, 2],
				[120, 3]
			],
			unobserved: [60],
			want: [[[0, 1]], [[120, 3]]]
		},
		{
			name: 'all points unobserved → nothing survives',
			points: [
				[0, 1],
				[60, 2]
			],
			unobserved: [0, 60],
			want: []
		},
		{
			name: 'leading and trailing gaps do not clip the surviving run',
			points: [
				[60, 2],
				[120, 3]
			],
			unobserved: [0, 180],
			want: [
				[
					[60, 2],
					[120, 3]
				]
			]
		}
	];

	for (const c of cases) {
		it(c.name, () => {
			expect(splitAtGaps(c.points, new Set(c.unobserved), STEP)).toEqual(c.want);
		});
	}

	it('step of 0 disables bucketing but never loops', () => {
		expect(splitAtGaps([[0, 1], [60, 2]], new Set([0]), 0)).toEqual([[[60, 2]]]);
	});
});
