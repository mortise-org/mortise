import { describe, expect, it } from 'vitest';
import { isFatalLogStreamEvent, parseLogStreamEvent } from './log-stream-events';

describe('isFatalLogStreamEvent', () => {
	it('returns true for fatal error events', () => {
		expect(isFatalLogStreamEvent({ kind: 'error', fatal: true })).toBe(true);
	});

	it('returns false for non-fatal error events', () => {
		expect(isFatalLogStreamEvent({ kind: 'error', fatal: false })).toBe(false);
		expect(isFatalLogStreamEvent({ kind: 'error', fatal: undefined })).toBe(false);
		expect(isFatalLogStreamEvent({ kind: 'error' })).toBe(false);
	});

	it('returns false for regular log events', () => {
		expect(isFatalLogStreamEvent({ kind: undefined, fatal: undefined })).toBe(false);
		expect(isFatalLogStreamEvent({ kind: 'info', fatal: true })).toBe(false);
	});

	it('returns false when kind is missing even if fatal is true', () => {
		expect(isFatalLogStreamEvent({ fatal: true })).toBe(false);
	});
});

describe('parseLogStreamEvent', () => {
	it('parses a JSON log event with all fields', () => {
		const data = JSON.stringify({
			pod: 'pod-1',
			ts: '2026-05-11T00:00:00Z',
			line: 'hello world',
			stream: 'stdout',
			kind: 'error',
			code: 'stream_open_failed',
			fatal: true
		});
		const result = parseLogStreamEvent(data);
		expect(result).toEqual({
			pod: 'pod-1',
			ts: '2026-05-11T00:00:00Z',
			line: 'hello world',
			stream: 'stdout',
			kind: 'error',
			code: 'stream_open_failed',
			fatal: true
		});
	});

	it('parses a JSON log event with only required fields', () => {
		const data = JSON.stringify({ line: 'just a line' });
		const result = parseLogStreamEvent(data);
		expect(result).toEqual({
			pod: '',
			ts: '',
			line: 'just a line',
			stream: undefined,
			kind: undefined,
			code: undefined,
			fatal: false
		});
	});

	it('returns a plain log line for non-JSON data', () => {
		const result = parseLogStreamEvent('plain text log line');
		expect(result).toEqual({
			pod: '',
			ts: '',
			line: 'plain text log line',
			stream: undefined
		});
	});

	it('returns a plain log line for empty strings', () => {
		const result = parseLogStreamEvent('');
		expect(result).toEqual({
			pod: '',
			ts: '',
			line: '',
			stream: undefined
		});
	});

	it('returns a plain log line for JSON without a line field', () => {
		const result = parseLogStreamEvent(JSON.stringify({ pod: 'pod-1', ts: 'now' }));
		expect(result).toEqual({
			pod: '',
			ts: '',
			line: '{"pod":"pod-1","ts":"now"}',
			stream: undefined
		});
	});
});
