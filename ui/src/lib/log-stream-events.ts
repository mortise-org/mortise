import type { LogLineEvent } from '$lib/types';

export function parseLogStreamEvent(data: string): LogLineEvent | null {
	try {
		const obj = JSON.parse(data);
		if (obj && typeof obj === 'object' && typeof obj.line === 'string') {
			const record = obj as Record<string, unknown>;
			const line = typeof record.line === 'string' ? record.line : data;
			return {
				pod: typeof record.pod === 'string' ? record.pod : '',
				ts: typeof record.ts === 'string' ? record.ts : '',
				line,
				stream: typeof record.stream === 'string' ? record.stream : undefined,
				kind: typeof record.kind === 'string' ? record.kind : undefined,
				code: typeof record.code === 'string' ? record.code : undefined,
				fatal: record.fatal === true
			};
		}
	} catch {
		/* not JSON */
	}
	return { pod: '', ts: '', line: data, stream: undefined };
}

export function isFatalLogStreamEvent(event: Pick<LogLineEvent, 'kind' | 'fatal'>): boolean {
	return event.kind === 'error' && event.fatal === true;
}
