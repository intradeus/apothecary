import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { aFlow, aVersion } from '$test/fixtures/flow.svelte';
import FlowVersions from './FlowVersions.svelte';

const current = 'abcdef0123456789';
const older = aVersion('0011223344556677');

const setup = (over: Partial<Parameters<typeof FlowVersions>[1]> = {}) => {
	const onload = vi.fn();
	const onclose = vi.fn();
	render(FlowVersions, {
		flow: aFlow({ hash: current }),
		versions: [aVersion(current), older],
		onload,
		onclose,
		...over
	});
	return { onload, onclose };
};

describe('FlowVersions', () => {
	it('lists each hash, the current one marked', () => {
		setup();
		expect(screen.getByText('@abcdef0123')).toBeInTheDocument();
		expect(screen.getByText('@0011223344')).toBeInTheDocument();
		expect(screen.getAllByText('current')).toHaveLength(1);
	});

	it('loads an older version', async () => {
		const { onload } = setup();
		await userEvent.click(screen.getByRole('button', { name: 'Load into editor' }));
		expect(onload).toHaveBeenCalledExactlyOnceWith(older);
	});

	it('offers no load for the current one', () => {
		setup({ versions: [aVersion(current)] });
		expect(screen.queryByRole('button', { name: 'Load into editor' })).not.toBeInTheDocument();
	});

	it('closes', async () => {
		const { onclose } = setup();
		await userEvent.click(screen.getByRole('button', { name: 'Close versions' }));
		expect(onclose).toHaveBeenCalledOnce();
	});

	it('says so with none', () => {
		setup({ versions: [] });
		expect(screen.getByText('No saved versions yet.')).toBeInTheDocument();
	});

	it('says a template has none of its own', () => {
		setup({ versions: [], flow: aFlow({ builtin: true }) });
		expect(
			screen.getByText('No saved versions yet — this is the built-in template.')
		).toBeInTheDocument();
	});
});
