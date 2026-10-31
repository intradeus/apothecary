import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Skillset, SkillsetRef, SkillsetVersion } from '$lib/domain';
import { bindable, choose } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import SkillsetPicker from './SkillsetPicker.svelte';

vi.mock('$lib/api', () => ({
	api: { skillsets: { list: vi.fn(), versions: vi.fn() } }
}));

const set = (over: Partial<Skillset> = {}) =>
	({ id: 's1', name: 'review', description: 'Code review', headSeq: 3, ...over }) as Skillset;
const version = (seq: number) =>
	({
		skillsetId: 's1',
		seq,
		hash: `abcdef12${seq}`,
		createdAt: '2026-10-01T00:00:00Z'
	}) as SkillsetVersion;

const setup = (value: SkillsetRef[] = []) => {
	const props = bindable({ value });
	render(SkillsetPicker, props);
	return props;
};

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.skillsets.list).mockResolvedValue([
		set(),
		set({ id: 's2', name: 'docs', description: '' })
	]);
	vi.mocked(api.skillsets.versions).mockResolvedValue([version(3), version(2)]);
});

describe('SkillsetPicker', () => {
	it('says it is loading', () => {
		vi.mocked(api.skillsets.list).mockReturnValue(deferred<Skillset[]>().promise);
		setup();
		expect(screen.getByText('Loading…')).toBeInTheDocument();
	});

	it('points to the library when there is nothing to pick', async () => {
		vi.mocked(api.skillsets.list).mockResolvedValue([]);
		setup();
		expect(await screen.findByRole('link', { name: 'Skills' })).toHaveAttribute(
			'href',
			'/system/skills'
		);
	});

	it('shows a failure to list', async () => {
		vi.mocked(api.skillsets.list).mockRejectedValue(new Error('down'));
		setup();
		expect(await screen.findByText('down')).toBeInTheDocument();
	});

	it('lists each set with its description, or its version without one', async () => {
		setup();
		expect(
			await screen.findByRole('checkbox', { name: /review.*Code review/ })
		).toBeInTheDocument();
		expect(screen.getByRole('checkbox', { name: /docs.*v3/ })).toBeInTheDocument();
	});

	it('picks a set at the latest version, and unpicks it', async () => {
		const props = setup();
		await userEvent.click(await screen.findByRole('checkbox', { name: /review/ }));
		expect(props.value).toEqual([{ id: 's1' }]);

		await userEvent.click(screen.getByRole('checkbox', { name: /review/ }));
		expect(props.value).toEqual([]);
	});

	it('loads versions once a set is ticked, once per set', async () => {
		const props = setup();
		await userEvent.click(await screen.findByRole('checkbox', { name: /review/ }));
		await waitFor(() => expect(api.skillsets.versions).toHaveBeenCalledWith('s1'));

		await userEvent.click(screen.getByRole('checkbox', { name: /docs/ }));
		await waitFor(() => expect(props.value).toHaveLength(2));
		await waitFor(() => expect(api.skillsets.versions).toHaveBeenCalledTimes(2));
	});

	it('pins a set to an older version, and back to the latest', async () => {
		const props = setup([{ id: 's1' }]);
		const select = await screen.findByRole('combobox', { name: 'review' });
		expect(select).toHaveTextContent('latest');

		await waitFor(() => expect(api.skillsets.versions).toHaveBeenCalled());
		await choose('review', /^v2/);
		expect(props.value).toEqual([{ id: 's1', seq: 2 }]);

		await choose('review', /^latest/);
		expect(props.value).toEqual([{ id: 's1' }]);
	});

	it('keeps a version pin when another set is picked', async () => {
		const props = setup([{ id: 's1', seq: 2 }]);
		await userEvent.click(await screen.findByRole('checkbox', { name: /docs/ }));
		expect(props.value).toEqual([{ id: 's1', seq: 2 }, { id: 's2' }]);
	});

	it('drops a pick of a set that is gone', async () => {
		const props = setup([{ id: 's1' }, { id: 'gone' }]);
		await screen.findByRole('checkbox', { name: /review/ });
		expect(props.value).toEqual([{ id: 's1' }]);
	});

	it('still offers the latest when versions fail to load', async () => {
		vi.mocked(api.skillsets.versions).mockRejectedValue(new Error('down'));
		setup([{ id: 's1' }]);
		const select = await screen.findByRole('combobox', { name: 'review' });
		await userEvent.click(select);
		expect(await screen.findByRole('option', { name: /latest/ })).toHaveTextContent('v3');
		expect(screen.getAllByRole('option')).toHaveLength(1);
	});
});
