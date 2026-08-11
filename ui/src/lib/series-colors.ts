// Fixed-order categorical series palette, validated (dataviz six checks) on
// the app's dark surface #181E18: lightness band, chroma floor, CVD
// separation, normal-vision floor, and contrast all pass. Slots are assigned
// by series index in a stable sort order — never hash-cycled — so the first
// 8 series are guaranteed distinct and a series keeps its color when the
// set changes. Past 8, seriesColor clamps to the last slot: callers that
// can exceed 8 must cap what they chart (see maxDistinctSeries) or accept
// the shared tail color.
const SERIES_COLORS = [
	'#3987e5', // blue
	'#d95926', // orange
	'#199e70', // aqua
	'#c98500', // yellow
	'#d55181', // magenta
	'#008300', // green
	'#9085e9', // violet
	'#e66767' // red
] as const;

// seriesColor returns the color for the series at index i of a STABLY SORTED
// series list. Beyond 8 series the palette does not extend — callers fold the
// tail into "Other" or facet; cycling hues is never correct.
export function seriesColor(i: number): string {
	return SERIES_COLORS[Math.min(i, SERIES_COLORS.length - 1)];
}

export const maxDistinctSeries = SERIES_COLORS.length;
