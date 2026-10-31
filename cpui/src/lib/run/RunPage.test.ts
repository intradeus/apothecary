import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { goto } from '$app/navigation';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { activeDesk } from '$lib/desk/active.svelte';
import type { EgressDenial, Run, RunEvent, RunServices } from '$lib/domain';
import { NEUTRAL_MIX, noise } from '$lib/ui/noise.svelte';
import { aRun } from '$test/fixtures';
import { aSample, settled } from '$test/fixtures/run-components';
import { DESK_KEY, sizeDesk, tab, windows } from '$test/fixtures/desk';
import { aLiveRun, anEvent, aRaise } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import RunPage from './RunPage.svelte';

// Every call a test does not set stays unanswered, so the fifteen real views
// sit in their loading states and what is left to see is the page's own work.
vi.mock('$lib/api', () => {
	const pending: unknown = Object.assign(() => {}, {
		then: () => pending,
		catch: () => pending,
		finally: () => pending
	});
	const group = () =>
		new Proxy({} as Record<string | symbol, unknown>, {
			get: (fns, name) => (fns[name] ??= vi.fn(() => pending))
		});
	return {
		api: new Proxy({} as Record<string | symbol, unknown>, {
			get: (groups, name) => (groups[name] ??= group())
		})
	};
});

vi.mock('@xterm/xterm', () => ({
	Terminal: class {
		loadAddon() {}
		open() {}
		onData() {}
		dispose() {}
		write() {}
	}
}));
vi.mock('@xterm/addon-fit', () => ({
	FitAddon: class {
		fit() {}
	}
}));

const close = vi.fn();

async function setup(run: Run | null = aLiveRun()) {
	if (run) vi.mocked(api.runs.get).mockResolvedValue({ run, events: [] });
	vi.mocked(api.runs.events).mockReturnValue(close);
	const onretried = vi.fn();
	const view = render(RunPage, { runId: run?.id ?? 'missing', onretried });
	await settled();
	const emit = (e: RunEvent) => vi.mocked(api.runs.events).mock.calls[0][2](e);
	return { ...view, onretried, emit };
}

const denial = (host: string): EgressDenial => ({
	host,
	count: 1,
	last: '2026-10-31T12:00:00Z',
	status: 'held'
});
const denied = (...hosts: string[]) =>
	vi.mocked(api.egress.denials).mockResolvedValue(hosts.map(denial));

const selected = (label: string) => expect(tab(label)).toHaveAttribute('aria-selected', 'true');
const countOn = (label: string) => tab(label).textContent;

beforeEach(() => {
	vi.clearAllMocks();
	localStorage.removeItem(DESK_KEY);
	sizeDesk();
	vi.mocked(api.runs.get).mockReset();
	vi.mocked(api.runs.files).mockResolvedValue({ snapshots: [], attachments: [] } as never);
	vi.mocked(api.egress.denials).mockResolvedValue([]);
	noise.blue = NEUTRAL_MIX;
});

afterEach(() => {
	activeDesk.desk = null;
});

describe('RunPage', () => {
	describe('before the run is here', () => {
		it('says it is connecting', async () => {
			vi.mocked(api.runs.get).mockReturnValue(deferred<never>().promise);
			await setup(null);
			expect(screen.getByText('Connecting…')).toBeInTheDocument();
			expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
			expect(document.title).toBe('Run · Apothecary');
		});

		it('shows why it could not load', async () => {
			vi.mocked(api.runs.get).mockRejectedValue(new Error('no such run'));
			await setup(null);
			expect(screen.getByText('This run could not be loaded.')).toBeInTheDocument();
			expect(screen.getByText('no such run')).toBeInTheDocument();
			expect(screen.queryByText('Connecting…')).not.toBeInTheDocument();
		});

		it('retries the load and carries on', async () => {
			vi.mocked(api.runs.get).mockRejectedValueOnce(new Error('blip'));
			await setup(null);

			vi.mocked(api.runs.get).mockResolvedValue({ run: aLiveRun(), events: [] });
			await userEvent.click(screen.getByRole('button', { name: /retry/i }));

			expect(await screen.findByRole('heading', { level: 1 })).toBeInTheDocument();
			expect(screen.queryByText('This run could not be loaded.')).not.toBeInTheDocument();
		});
	});

	describe('its lifetime', () => {
		it('titles the tab with the run', async () => {
			await setup(aLiveRun({ title: 'Ship it' }));
			expect(document.title).toBe('r_abcd · Ship it · Apothecary');
		});

		it('closes the stream when it leaves', async () => {
			const { unmount } = await setup();
			expect(close).not.toHaveBeenCalled();
			unmount();
			expect(close).toHaveBeenCalledOnce();
		});

		it('hands its desk to the sidebar while it is here', async () => {
			const { unmount } = await setup();
			expect(activeDesk.desk).not.toBeNull();
			unmount();
			expect(activeDesk.desk).toBeNull();
		});

		it.each<[Run['state'], number]>([
			['running', 20],
			['provisioning', 20],
			['waiting_on_human', 80],
			['paused', NEUTRAL_MIX],
			['completed', NEUTRAL_MIX],
			['failed', NEUTRAL_MIX]
		])('colours the backdrop for a %s run', async (state, blue) => {
			await setup(aLiveRun({ state }));
			expect(noise.blue).toBe(blue);
		});

		it('hands the backdrop back when it leaves', async () => {
			const { unmount } = await setup(aLiveRun({ state: 'running' }));
			unmount();
			expect(noise.blue).toBe(NEUTRAL_MIX);
		});

		it('follows the run as its state changes', async () => {
			const { emit } = await setup(aLiveRun({ state: 'running' }));
			vi.mocked(api.runs.get).mockResolvedValue({
				run: aLiveRun({ state: 'waiting_on_human' }),
				events: []
			});
			emit(anEvent(1, 'run_paused'));
			await waitFor(() => expect(noise.blue).toBe(80));
		});
	});

	describe('the windows', () => {
		it('starts on the split layout', async () => {
			await setup();
			expect(windows()[0]).toEqual(['Flow']);
			expect(windows().length).toBeGreaterThan(2);
		});

		it('gives a phone one window with every view', async () => {
			sizeDesk(600, 800);
			await setup();
			await waitFor(() => expect(windows()).toHaveLength(1));
			expect(windows()[0]).toHaveLength(15);
		});

		it('names the flow and its version', async () => {
			await setup(aLiveRun({ flowName: 'builder', flowHash: 'e7c90c12' }));
			expect(screen.getByText('builder @e7c90c')).toBeInTheDocument();
		});

		it('names a flow with no version', async () => {
			await setup(aLiveRun({ flowName: 'builder', flowHash: '' }));
			expect(screen.getByText('builder')).toBeInTheDocument();
		});

		it.each<[string, Run['state'], string]>([
			['live', 'running', 'following'],
			['finished', 'completed', 'closed']
		])('says a %s session is %s', async (_, state, hint) => {
			await setup(aLiveRun({ state }));
			expect(screen.getByText(hint)).toBeInTheDocument();
		});

		it('counts the pings waiting and marks the tab', async () => {
			const { emit } = await setup();
			emit(aRaise(1, 'p1'));
			emit(aRaise(2, 'p2'));
			await waitFor(() => expect(countOn('Pings')).toContain('2'));
			expect(tab('Pings').querySelector('.count')).toHaveClass('hot');
		});

		it('counts every ping, unmarked, once none waits', async () => {
			const { emit } = await setup();
			emit(aRaise(1, 'p1'));
			emit(anEvent(2, 'ping_answered', { ping_id: 'p1', answer: 'approve' }));
			await waitFor(() => expect(countOn('Pings')).toContain('1'));
			expect(tab('Pings').querySelector('.count')).not.toHaveClass('hot');
		});

		it('leaves the transcript out of the timeline count', async () => {
			const { emit } = await setup();
			emit(anEvent(1, 'stage_started', { stage_id: 'a' }));
			emit(anEvent(2, 'agent_message', { segment: 's', text: 'Hi' }));
			emit(anEvent(3, 'agent_tool', { tool_call_id: 't' }));
			await waitFor(() => expect(countOn('Timeline')).toContain('1'));
			expect(countOn('Timeline')).not.toContain('3');
		});

		it('counts the record: what was captured and what was attached', async () => {
			vi.mocked(api.runs.files).mockResolvedValue({
				snapshots: [{ id: 's1' }, { id: 's2' }],
				attachments: [{ id: 'a1' }]
			} as never);
			await setup();
			expect(countOn('Record')).toContain('3');
		});

		it('counts held hosts on the Egress tab and marks it', async () => {
			denied('a.test', 'b.test');
			await setup();
			expect(countOn('Egress')).toContain('2');
			expect(tab('Egress').querySelector('.count')).toHaveClass('hot');
		});

		describe('the Services tab', () => {
			const services = (...states: string[]) =>
				vi.mocked(api.runs.services).mockResolvedValue({
					workspace: true,
					services: states.map((state, i) => ({
						name: `s${i}`,
						state,
						ports: []
					}))
				} as unknown as RunServices);

			it('counts the healthy', async () => {
				services('healthy', 'healthy', 'starting');
				await setup();
				expect(countOn('Services')).toContain('2');
				expect(tab('Services').querySelector('.count')).not.toHaveClass('bad');
			});

			it('raises the alarm for an unhealthy one', async () => {
				services('healthy', 'unhealthy');
				await setup();
				expect(tab('Services').querySelector('.count')).toHaveClass('bad');
			});
		});

		it('counts the ports seen', async () => {
			vi.mocked(api.runs.ports).mockResolvedValue({
				exposed: [],
				observed: [{ port: 3000 }, { port: 4000 }]
			} as never);
			await setup();
			expect(countOn('Ports')).toContain('2');
		});
	});

	describe('held hosts', () => {
		it('asks about them, by name', async () => {
			denied('a.test', 'b.test');
			await setup();
			expect(screen.getByText(/^Egress held/)).toBeInTheDocument();
			expect(screen.getByText(/a\.test/)).toBeInTheDocument();
			expect(screen.getByText(/b\.test/)).toBeInTheDocument();
		});

		it('says nothing with none', async () => {
			await setup();
			expect(screen.queryByText(/^Egress held/)).not.toBeInTheDocument();
		});

		it('opens the Egress window on the held hosts', async () => {
			denied('a.test');
			await setup();
			await userEvent.click(tab('Services'));

			await userEvent.click(screen.getByRole('button', { name: 'Review' }));

			selected('Egress');
			expect(await screen.findByRole('radio', { name: /^Held/ })).toHaveAttribute(
				'aria-checked',
				'true'
			);
		});

		it('snoozes on dismiss, without ruling on them', async () => {
			denied('a.test');
			await setup();
			await userEvent.click(screen.getByRole('button', { name: /dismiss/i }));
			expect(screen.queryByText(/^Egress held/)).not.toBeInTheDocument();
			expect(api.egress.denials).toHaveBeenCalled();
		});

		it('stops asking once the run is over', async () => {
			denied('a.test');
			const { emit } = await setup();
			expect(screen.getByText(/^Egress held/)).toBeInTheDocument();

			vi.mocked(api.runs.get).mockResolvedValue({
				run: aLiveRun({ state: 'completed' }),
				events: []
			});
			emit(anEvent(1, 'run_completed'));

			await waitFor(() => expect(screen.queryByText(/^Egress held/)).not.toBeInTheDocument());
		});
	});

	describe('a workspace in an unknown state', () => {
		const lost = () => aLiveRun({ envStatus: 'unknown' });

		it('says so', async () => {
			await setup(lost());
			expect(screen.getByText('Workspace unknown')).toBeInTheDocument();
			expect(
				screen.getByText(/Provisioner unreachable — check the integration token/)
			).toBeInTheDocument();
		});

		it('says nothing of a healthy one', async () => {
			await setup();
			expect(screen.queryByText('Workspace unknown')).not.toBeInTheDocument();
		});

		it('points at the integrations', async () => {
			await setup(lost());
			await userEvent.click(screen.getByRole('button', { name: 'Integrations' }));
			expect(goto).toHaveBeenCalledWith('/system/integrations');
		});

		it('can be dismissed', async () => {
			await setup(lost());
			await userEvent.click(screen.getByRole('button', { name: /dismiss/i }));
			expect(screen.queryByText('Workspace unknown')).not.toBeInTheDocument();
		});
	});

	describe('a failed action', () => {
		async function fail() {
			vi.mocked(api.runs.pause).mockRejectedValue(new Error('the engine refused'));
			const view = await setup(aLiveRun({ state: 'running' }));
			await userEvent.click(screen.getByRole('button', { name: /^pause/i }));
			return view;
		}

		it('shows under the header', async () => {
			await fail();
			expect(await screen.findByText('the engine refused')).toBeInTheDocument();
		});

		it('can be dismissed', async () => {
			await fail();
			const cause = await screen.findByText('the engine refused');
			const card = cause.closest('[role="alert"]') as HTMLElement;
			await userEvent.click(within(card).getByRole('button', { name: /dismiss/i }));
			expect(screen.queryByText('the engine refused')).not.toBeInTheDocument();
		});
	});

	describe('a stage pill', () => {
		const RECIPE = 'name: f\nversion: 5\nmain:\n  - { id: build, type: agent }\n';
		const inRecipe = () => aLiveRun({ projectId: 'p1' });
		const pill = () => screen.findByTitle(/build · agent/);
		const started = anEvent(1, 'stage_started', { stage_id: 'build', iteration: 1 });

		beforeEach(() => {
			vi.mocked(api.flows.version).mockResolvedValue({ yaml: RECIPE } as never);
		});

		it('opens the session of a stage still going', async () => {
			const { emit } = await setup(inRecipe());
			emit(started);
			await userEvent.click(tab('Timeline'));
			selected('Timeline');

			await userEvent.click(await pill());

			selected('Session');
		});

		it('opens the session of a stage not started at all', async () => {
			await setup(inRecipe());
			await userEvent.click(tab('Timeline'));
			await userEvent.click(await pill());
			selected('Session');
		});

		it.each(['stage_completed', 'stage_failed', 'stage_paused'])(
			'opens the log once it has %s',
			async (type) => {
				const { emit } = await setup(inRecipe());
				emit(started);
				emit(anEvent(2, type, { stage_id: 'build', duration_ms: 1000 }));

				await userEvent.click(await pill());

				selected('Logs');
			}
		);

		it("does not count another stage's finish", async () => {
			const { emit } = await setup(inRecipe());
			emit(started);
			emit(anEvent(2, 'stage_completed', { stage_id: 'other', duration_ms: 1000 }));
			await userEvent.click(tab('Timeline'));

			await userEvent.click(await pill());

			selected('Session');
		});
	});

	describe('a window with nothing behind it', () => {
		const toRecord = () => screen.queryAllByRole('button', { name: 'Open the Record' });
		const goesAway = (emit: (e: RunEvent) => void) => {
			vi.mocked(api.runs.get).mockResolvedValue({
				run: aLiveRun({ envStatus: 'absent' }),
				events: []
			});
			emit(anEvent(1, 'env_destroyed'));
		};

		it('sends Vitals to the Record', async () => {
			await setup(aRun());
			expect(screen.getByText('No workspace yet')).toBeInTheDocument();
			await userEvent.click(toRecord()[0]);
			selected('Record');
		});

		it('does the same on Ports', async () => {
			await setup(aRun());
			expect(toRecord()).toHaveLength(1);
			await userEvent.click(tab('Ports'));
			await waitFor(() => expect(toRecord()).toHaveLength(2));
		});

		it('shows the real windows while there is a workspace', async () => {
			await setup();
			await userEvent.click(tab('Ports'));
			await settled();
			expect(toRecord()).toEqual([]);
		});

		// The samples outlive the workspace on this page, so a run watched to its
		// end keeps its graphs; ports were live facts and have nothing left.
		it('keeps the graphs of a workspace that went away while watching', async () => {
			vi.mocked(api.runs.resources).mockResolvedValue([aSample(), aSample()]);
			const { emit } = await setup();
			await userEvent.click(tab('Ports'));
			await settled();

			goesAway(emit);

			await waitFor(() => expect(toRecord()).toHaveLength(1));
		});

		it('gives way too with too few samples to draw', async () => {
			vi.mocked(api.runs.resources).mockResolvedValue([aSample()]);
			const { emit } = await setup();
			await userEvent.click(tab('Ports'));
			await settled();

			goesAway(emit);

			await waitFor(() => expect(toRecord()).toHaveLength(2));
		});
	});
});
