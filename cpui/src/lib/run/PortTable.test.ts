import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import PortTable, { type PortRow } from './PortTable.svelte';

const URL = 'https://4000.preview.test/';
const headers = () => screen.getAllByRole('columnheader').map((h) => h.textContent);
const cells = (port: number) =>
	within(screen.getByText(`:${port}`).closest('tr')!)
		.getAllByRole('cell')
		.map((c) => c.textContent?.trim());
const listening = (port: number, over: Partial<PortRow> = {}): PortRow => ({
	port,
	status: 'listening',
	exposed: false,
	...over
});
const exposed = (port: number, over: Partial<PortRow> = {}): PortRow => ({
	port,
	url: URL,
	status: 'exposed',
	exposed: true,
	...over
});
const forward = (port: number) => `apothecary forward ws ${port}`;

describe('PortTable', () => {
	it('has every column it is given, in order', () => {
		render(PortTable, {
			rows: [listening(5432, { name: 'db' })],
			nameHeader: 'Service',
			forward,
			links: true
		});
		expect(headers()).toEqual(['Service', 'Port', 'Status', 'Your machine', 'Browser']);
		expect(cells(5432).slice(0, 3)).toEqual(['db', ':5432', 'listening']);
	});

	it('drops the name, forward and browser columns it is not given', () => {
		render(PortTable, { rows: [listening(5432)], links: false });
		expect(headers()).toEqual(['Port', 'Status']);
		expect(cells(5432)).toEqual([':5432', 'listening']);
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});

	it('marks a nameless port, tints an exposed one, and carries the titles', () => {
		render(PortTable, {
			rows: [
				listening(9000, { statusTitle: 'not yet', portTitle: 'http://localhost:9000/' }),
				exposed(4000)
			],
			nameHeader: 'Service',
			links: true
		});
		expect(cells(9000)[0]).toBe('—');
		expect(screen.getByText('listening')).not.toHaveClass('tint-success');
		expect(screen.getByText('listening')).toHaveAttribute('title', 'not yet');
		expect(screen.getByText(':9000')).toHaveAttribute('title', 'http://localhost:9000/');
		expect(screen.getByText('exposed')).toHaveClass('tint-success');
	});

	it('copies a port’s forward command from a button that says forward', async () => {
		const user = userEvent.setup();
		const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
		render(PortTable, { rows: [listening(5432)], forward, links: false });

		const btn = screen.getByRole('button', { name: 'forward: copy apothecary forward ws 5432' });
		expect(btn).toHaveTextContent('forward');
		await user.click(btn);

		expect(write).toHaveBeenCalledExactlyOnceWith('apothecary forward ws 5432');
	});

	it('links an exposed port in a new tab', () => {
		render(PortTable, { rows: [exposed(4000)], links: true });
		const link = screen.getByRole('link', { name: 'Open :4000 in the browser' });
		expect(link).toHaveAttribute('href', URL);
		expect(link).toHaveAttribute('target', '_blank');
	});

	it('opens a port without a link through the caller, which can hold every button', async () => {
		const onopen = vi.fn();
		const { rerender } = render(PortTable, {
			rows: [listening(4000), listening(5000)],
			links: true,
			onopen
		});
		const open = (port: number) =>
			screen.getByRole('button', { name: `Open :${port} in the browser` });

		await userEvent.click(open(5000));
		expect(onopen).toHaveBeenCalledExactlyOnceWith(5000);

		await rerender({ busy: 5000 });
		expect(open(5000)).toBeDisabled();
		expect(open(5000)).toHaveAttribute('aria-busy', 'true');
		expect(open(4000)).toBeDisabled();
		expect(open(4000)).toHaveAttribute('aria-busy', 'false');
	});

	it('offers no open for a port without a link when the caller cannot expose', () => {
		render(PortTable, { rows: [listening(4000)], links: true });
		expect(headers()).toContain('Browser');
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});
});
