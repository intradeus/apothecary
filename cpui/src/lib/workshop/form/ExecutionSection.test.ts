import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { anIntegration, bindable, choose } from '$test/fixtures/app.svelte';
import ExecutionSection from './ExecutionSection.svelte';

const docker = anIntegration({ id: 'd', name: 'Docker', port: 'provisioner', kind: 'docker' });
const k8s = anIntegration({ id: 'k', name: 'K8s', port: 'provisioner', kind: 'kubernetes' });
const claudeA = anIntegration({ id: 'a', name: 'Claude A', port: 'harness', kind: 'claude' });
const claudeB = anIntegration({ id: 'b', name: 'Claude B', port: 'harness', kind: 'claude' });
const codex = anIntegration({ id: 'c', name: 'Codex', port: 'harness', kind: 'codex' });

const setup = (over: Record<string, unknown> = {}) => {
	const props = bindable({
		provisioners: [docker, k8s],
		harnesses: [claudeA, claudeB, codex],
		provisionerId: 'd',
		harnessIds: ['a'],
		...over
	});
	render(ExecutionSection, props);
	return props;
};
const box = (name: RegExp) => screen.getByRole('checkbox', { name });

describe('ExecutionSection', () => {
	it('binds the provisioner', async () => {
		const props = setup();
		await choose('Provisioner', 'K8s (kubernetes)');
		expect(props.provisionerId).toBe('k');
	});

	it('links to the integrations with no provisioner', () => {
		setup({ provisioners: [], provisionerId: '' });
		const links = screen.getAllByRole('link');
		expect(links[0]).toHaveAttribute('href', '/system/integrations');
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
});
