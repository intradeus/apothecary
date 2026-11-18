import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { flushSync } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import type { RunCheckout } from '$lib/domain';
import { ComposerRegistry, type ComposerKind } from './composerTarget.svelte';
import AnnotateBox from './AnnotateBox.svelte';

const checkout = (name: string, primary = false): RunCheckout => ({
	name,
	dir: primary ? 'repo' : `repos/${name}`,
	url: `https://git.test/dev/${name}`,
	branch: 'apothecary/run',
	source: 'main',
	primary
});

function setup(kinds: ComposerKind[] = [], over: { oncancel?: () => void } = {}) {
	const registry = new ComposerRegistry();
	const targets = kinds.map((kind) => ({ kind, add: vi.fn() }));
	targets.forEach((tg) => registry.register(tg));
	const onsent = vi.fn();
	render(AnnotateBox, {
		registry,
		loc: { repo: 'web', path: 'src/app.ts' },
		from: 4,
		to: 6,
		quote: 'a\nb\nc',
		checkouts: [checkout('api', true), checkout('web')],
		onsent,
		...over
	});
	return { registry, targets, onsent };
}

const field = () => screen.getByRole('textbox', { name: 'Comment on web:src/app.ts:4-6' });

describe('AnnotateBox', () => {
	it('is off, saying why, with no chat to send to', () => {
		setup();
		expect(screen.getByRole('group', { name: 'web:src/app.ts:4-6' })).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Send to Session' })).toBeDisabled();
		expect(
			screen.getByText('Take over the agent or open Ask to send this to a chat')
		).toBeInTheDocument();
	});

	it('sends to the held session, preferred over Ask', async () => {
		const { targets, onsent } = setup(['ask', 'hold']);
		const [ask, hold] = targets;
		expect(screen.queryByText(/^Take over the agent/)).not.toBeInTheDocument();

		await userEvent.type(field(), 'Rename these');
		await userEvent.click(screen.getByRole('button', { name: 'Send to Session' }));

		expect(hold.add).toHaveBeenCalledExactlyOnceWith(
			expect.objectContaining({
				ref: 'web:src/app.ts:4-6',
				lang: 'ts',
				quote: 'a\nb\nc',
				comment: 'Rename these'
			})
		);
		expect(ask.add).not.toHaveBeenCalled();
		expect(onsent).toHaveBeenCalledExactlyOnceWith('hold');
		expect(field()).toHaveValue('');
	});

	it('sends to Ask on Enter when Ask is all there is', async () => {
		const { targets, onsent } = setup(['ask']);
		expect(screen.getByRole('button', { name: 'Send to Ask' })).toBeEnabled();

		await userEvent.type(field(), 'Why?{Enter}');

		expect(targets[0].add).toHaveBeenCalledExactlyOnceWith(
			expect.objectContaining({ comment: 'Why?' })
		);
		expect(onsent).toHaveBeenCalledExactlyOnceWith('ask');
	});

	it('follows the registry as chats come and go', () => {
		const { registry } = setup();
		const drop = registry.register({ kind: 'ask', add: vi.fn() });
		flushSync();
		expect(screen.getByRole('button', { name: 'Send to Ask' })).toBeEnabled();

		drop();
		flushSync();
		expect(screen.getByRole('button', { name: 'Send to Session' })).toBeDisabled();
	});

	it('sends nothing on Enter with no chat to send to', async () => {
		const { onsent } = setup();
		await userEvent.type(field(), 'x{Enter}');
		expect(onsent).not.toHaveBeenCalled();
		expect(field()).toHaveValue('x');
	});

	it('keeps writing on Shift+Enter and leaves an IME Enter alone', async () => {
		const { targets } = setup(['hold']);
		await userEvent.type(field(), 'one{Shift>}{Enter}{/Shift}two');
		await fireEvent.keyDown(field(), { key: 'Enter', isComposing: true });

		expect(field()).toHaveValue('one\ntwo');
		expect(targets[0].add).not.toHaveBeenCalled();
	});

	it('lets the selection go from Cancel and on Escape', async () => {
		const oncancel = vi.fn();
		setup(['hold'], { oncancel });

		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		await userEvent.type(field(), '{Escape}');

		expect(oncancel).toHaveBeenCalledTimes(2);
	});

	it('offers no Cancel without a way to cancel, and ignores Escape', async () => {
		const { targets } = setup(['hold']);
		expect(screen.queryByRole('button', { name: 'Cancel' })).not.toBeInTheDocument();
		await userEvent.type(field(), '{Escape}');
		expect(targets[0].add).not.toHaveBeenCalled();
	});
});
