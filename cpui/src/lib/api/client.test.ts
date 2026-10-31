import { describe, expect, it } from 'vitest';
import { unwrap, unwrapOptional, unwrapVoid } from './client';

const ok = new Response(null, { status: 200 });
const failed = (status: number, statusText = '') => new Response(null, { status, statusText });
const failure = (error: unknown, response?: Response) =>
	Promise.resolve({ data: undefined, error, response });

describe('unwrap', () => {
	it('resolves to the body', async () => {
		const call = Promise.resolve({ data: { id: 1 }, error: undefined, response: ok });
		await expect(unwrap(call)).resolves.toEqual({ id: 1 });
	});

	it("throws the engine's own message", async () => {
		await expect(unwrap(failure({ error: 'run is archived' }, failed(409)))).rejects.toThrow(
			'run is archived'
		);
	});

	it('throws a bare string error as it came', async () => {
		await expect(unwrap(failure('bad gateway', failed(502)))).rejects.toThrow('bad gateway');
	});

	it('falls back to the status line when the body says nothing', async () => {
		await expect(unwrap(failure({}, failed(503, 'Unavailable')))).rejects.toThrow(
			'503 Unavailable'
		);
	});

	it('names the outage when no response arrived at all', async () => {
		await expect(unwrap(failure(new TypeError('failed')))).rejects.toThrow(/reach/i);
	});

	it('treats a non-ok response with no error as a failure', async () => {
		const call = Promise.resolve({ data: {}, error: undefined, response: failed(500) });
		await expect(unwrap(call)).rejects.toThrow('500');
	});
});

describe('unwrapVoid', () => {
	it('resolves on success and throws on failure', async () => {
		const call = Promise.resolve({ data: undefined, error: undefined, response: ok });
		await expect(unwrapVoid(call)).resolves.toBeUndefined();
		await expect(unwrapVoid(failure({ error: 'no' }, failed(400)))).rejects.toThrow('no');
	});
});

describe('unwrapOptional', () => {
	it('answers null for a failure instead of throwing', async () => {
		await expect(unwrapOptional(failure({ error: 'no gateway' }, failed(404)))).resolves.toBeNull();
	});

	it('answers null for an empty body', async () => {
		const call = Promise.resolve({ data: undefined, error: undefined, response: ok });
		await expect(unwrapOptional(call)).resolves.toBeNull();
	});

	it('resolves to the body', async () => {
		const call = Promise.resolve({ data: 'log', error: undefined, response: ok });
		await expect(unwrapOptional(call)).resolves.toBe('log');
	});
});
