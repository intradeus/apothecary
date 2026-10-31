import { describe, expect, it, vi } from 'vitest';
import { deferred } from '$test/helpers';
import { HeaderActions } from './actions.svelte';

describe('HeaderActions', () => {
	it('holds an action while it runs and clears it after', async () => {
		const actions = new HeaderActions();
		const job = deferred<boolean>();
		const running = actions.run('rebuild', () => job.promise);

		expect(actions.has('rebuild')).toBe(true);
		expect(actions.any).toBe(true);

		job.resolve(true);
		await expect(running).resolves.toBe(true);
		expect(actions.has('rebuild')).toBe(false);
		expect(actions.any).toBe(false);
	});

	it('keeps a long action held when a shorter one ends inside it', async () => {
		const actions = new HeaderActions();
		const rebuild = deferred<boolean>();
		const running = actions.run('rebuild', () => rebuild.promise);

		await actions.run('editor', async () => true);

		expect(actions.has('editor')).toBe(false);
		expect(actions.has('rebuild')).toBe(true);
		expect(actions.any).toBe(true);

		rebuild.resolve(true);
		await running;
		expect(actions.any).toBe(false);
	});

	it('runs a second click of the same action not at all', async () => {
		const actions = new HeaderActions();
		const job = deferred<boolean>();
		const again = vi.fn(async () => true);
		const running = actions.run('stop', () => job.promise);

		await expect(actions.run('stop', again)).resolves.toBe(false);
		expect(again).not.toHaveBeenCalled();

		job.resolve(true);
		await running;
	});

	it('clears an action that throws, and lets the throw through', async () => {
		const actions = new HeaderActions();
		await expect(
			actions.run('editor', async () => {
				throw new Error('boom');
			})
		).rejects.toThrow('boom');
		expect(actions.any).toBe(false);
	});
});
