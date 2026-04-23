const POD_COLORS = [
	'#a78bfa',
	'#22d3ee',
	'#4ade80',
	'#fbbf24',
	'#fb7185',
	'#c08050',
	'#f472b6',
	'#34d399'
];

export function hashPodColor(pod: string): string {
	let h = 0;
	for (let i = 0; i < pod.length; i++) {
		h = (h * 31 + pod.charCodeAt(i)) | 0;
	}
	return POD_COLORS[Math.abs(h) % POD_COLORS.length];
}
