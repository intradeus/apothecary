import { flushSync } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import { ComposerRegistry, type ComposerKind, type ComposerTarget } from './composerTarget.svelte';

const target = (kind: ComposerKind): ComposerTarget => ({ kind, add: vi.fn() });
const REASON = 'Take over the agent or open Ask to send this to a chat';

describe('ComposerRegistry', () => {
	it('has nowhere to send to, and says why, with nothing registered', () => {
		const reg = new ComposerRegistry();
		expect(reg.active).toBeNull();
		expect(reg.reason).toBe(REASON);
	});

	it('sends to Ask when it is the only one, with no reason', () => {
		const reg = new ComposerRegistry();
		const ask = target('ask');
		reg.register(ask);
		expect(reg.active).toBe(ask);
		expect(reg.reason).toBe('');
	});

	it('prefers the held session over Ask, whichever registered first', () => {
		const reg = new ComposerRegistry();
		const ask = target('ask');
		const hold = target('hold');
		reg.register(hold);
		reg.register(ask);
		expect(reg.active).toBe(hold);

		const other = new ComposerRegistry();
		other.register(ask);
		other.register(hold);
		expect(other.active).toBe(hold);
	});

	it('falls back to the other when one unregisters', () => {
		const reg = new ComposerRegistry();
		const ask = target('ask');
		const hold = target('hold');
		const dropAsk = reg.register(ask);
		const dropHold = reg.register(hold);

		dropHold();
		expect(reg.active).toBe(ask);
		reg.register(hold);
		dropAsk();
		expect(reg.active).toBe(hold);
	});

	it('keeps the live one of a kind registered twice when the stale one unregisters', () => {
		const reg = new ComposerRegistry();
		const old = target('hold');
		const fresh = target('hold');
		const dropOld = reg.register(old);
		const dropFresh = reg.register(fresh);
		expect(reg.active).toBe(fresh);

		dropOld();
		expect(reg.active).toBe(fresh);
		dropFresh();
		expect(reg.active).toBeNull();
		expect(reg.reason).toBe(REASON);
	});

	it('falls back to the earlier one of a kind when the latest goes', () => {
		const reg = new ComposerRegistry();
		const old = target('ask');
		reg.register(old);
		const drop = reg.register(target('ask'));
		drop();
		expect(reg.active).toBe(old);
	});

	it('drops only its own registration, however often unregister is called', () => {
		const reg = new ComposerRegistry();
		const hold = target('hold');
		const first = reg.register(hold);
		reg.register(hold);

		first();
		first();
		expect(reg.active).toBe(hold);
	});

	it('is reactive: an effect sees the target change, and only a change', () => {
		const reg = new ComposerRegistry();
		const seen: (ComposerKind | null)[] = [];
		const stop = $effect.root(() => {
			$effect(() => {
				seen.push(reg.active?.kind ?? null);
			});
		});
		flushSync();
		const drop = reg.register(target('ask'));
		flushSync();
		reg.register(target('hold'));
		flushSync();
		drop();
		flushSync();
		stop();
		expect(seen).toEqual([null, 'ask', 'hold']);
	});
});
