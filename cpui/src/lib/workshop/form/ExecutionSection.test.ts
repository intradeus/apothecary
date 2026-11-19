import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { anIntegration, bindable } from '$test/fixtures/app.svelte';
import ExecutionSection from './ExecutionSection.svelte';

const claudeA = anIntegration({ id: 'a', name: 'Claude A', port: 'harness', kind: 'claude' });
const claudeB = anIntegration({ id: 'b', name: 'Claude B', port: 'harness', kind: 'claude' });
const codex = anIntegration({ id: 'c', name: 'Codex', port: 'harness', kind: 'codex' });

const setup = (over: Record<string, unknown> = {}) => {
	const props = bindable({
		harnesses: [claudeA, claudeB, codex],
		harnessIds: ['a'],
		...over
	});
	render(ExecutionSection, props);
	return props;
};
const box = (name: RegExp) => screen.getByRole('checkbox', { name });

describe('ExecutionSection', () => {
	it('offers no provisioner: workspaces are built on the instance’s', () => {
		setup();
		expect(screen.queryByText(/provisioner/i)).not.toBeInTheDocument();
	});

	it('links to the integrations with no harness', () => {
		setup({ harnesses: [], harnessIds: [] });
		expect(screen.getByRole('link')).toHaveAttribute('href', '/system/integrations');
	});

	it('picks and unpicks a harness', async () => {
		const props = setup();
		await userEvent.click(box(/Codex/));
		expect(props.harnessIds).toEqual(['a', 'c']);
		expect(box(/Codex/)).toBeChecked();

		await userEvent.click(box(/Claude A/));
		expect(props.harnessIds).toEqual(['c']);
	});

	it('allows one harness per kind, and says why', async () => {
		setup();
		expect(box(/Claude B/)).toBeDisabled();
		expect(box(/Claude B/)).toHaveAttribute(
			'title',
			'Another claude integration is already picked'
		);
		expect(box(/Claude B/)).toHaveTextContent('kind taken');
		expect(box(/Claude A/)).toBeEnabled();
		expect(box(/Codex/)).toBeEnabled();
	});

	it('frees the kind when its harness is unpicked', async () => {
		setup();
		await userEvent.click(box(/Claude A/));
		expect(box(/Claude B/)).toBeEnabled();
	});

	it('counts what is picked', () => {
		setup({ harnessIds: ['a', 'c'] });
		expect(
			screen.getByRole('heading', { name: 'Harness integrations' }).nextElementSibling
		).toHaveTextContent('2');
	});

	it('keeps a shared workshop’s harnesses, offering none of the caller’s', () => {
		const props = setup({ harnessIds: ['owners', 'owners-2'], shared: true });
		expect(screen.queryByRole('checkbox')).toBeNull();
		expect(screen.queryByRole('link')).toBeNull();
		expect(
			screen.getByText(
				'It runs on 2 harness integrations of its owner’s, kept as they are: only the owner changes them.'
			)
		).toBeInTheDocument();
		expect(props.harnessIds).toEqual(['owners', 'owners-2']);
	});
});
