import { afterEach, describe, expect, it } from 'vitest';
import { confirmDialog, confirmState } from './confirm.svelte';

const asked = { title: 'Stop this run?', body: 'It stops.', action: 'Stop run' };

describe('confirmDialog', () => {
	afterEach(() => {
		confirmState.settle(false);
	});

	it('puts the question up with everything it was asked', () => {
		void confirmDialog({ ...asked, tone: 'warn', cancel: 'Keep it' });
		expect(confirmState.current).toMatchObject({ ...asked, tone: 'warn', cancel: 'Keep it' });
	});

	it('resolves true on the action and takes the question down', async () => {
		const answer = confirmDialog(asked);
		confirmState.settle(true);
		await expect(answer).resolves.toBe(true);
		expect(confirmState.current).toBeNull();
	});

	it('resolves false on a cancel', async () => {
		const answer = confirmDialog(asked);
		confirmState.settle(false);
		await expect(answer).resolves.toBe(false);
		expect(confirmState.current).toBeNull();
	});

	it('stays pending until someone answers', async () => {
		let answered = false;
		void confirmDialog(asked).then(() => (answered = true));
		await Promise.resolve();
		expect(answered).toBe(false);
	});

	it('refuses the waiting question when a second one arrives', async () => {
		const first = confirmDialog(asked);
		const second = confirmDialog({ ...asked, title: 'Archive it?' });

		await expect(first).resolves.toBe(false);
		expect(confirmState.current?.title).toBe('Archive it?');

		confirmState.settle(true);
		await expect(second).resolves.toBe(true);
	});

	it('takes a settle with nothing waiting as nothing', () => {
		expect(() => confirmState.settle(true)).not.toThrow();
		expect(confirmState.current).toBeNull();
	});

	it('answers a question once, however many clicks arrive', async () => {
		const answer = confirmDialog(asked);
		confirmState.settle(true);
		confirmState.settle(false);
		await expect(answer).resolves.toBe(true);
	});
});
