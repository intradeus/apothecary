import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { to } from '$lib/core/urls';
import type { RunServices, ServiceStatus } from '$lib/domain';
import { confirmState } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { aProject, aRun } from '$test/fixtures';
import { AT, NOW, aLiveRun, anEvent, aService, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import ServicesView from './ServicesView.svelte';

vi.mock('$lib/api', () => ({
	api: {
		runs: {
			services: vi.fn(),
			startService: vi.fn(),
			restartService: vi.fn(),
			stopService: vi.fn(),
			serviceLog: vi.fn()
		}
	}
}));

const RUN = '01TESTRUN0000000000000ABCD';
const HOUR_AGO = new Date(NOW - 3_600_000).toISOString();
const btn = (name: string | RegExp) => screen.getByRole('button', { name });
const noBtn = (name: string | RegExp) => screen.queryByRole('button', { name });
const card = (name: string) => within(screen.getByText(name).closest('article')!);

interface Opts {
	rs?: ReturnType<typeof aViewStore>;
	workspace?: boolean;
	error?: string;
	onstage?: (id: string) => void;
	visible?: boolean;
}

/** Opens the view on a list the feed goes on answering. */
async function open(services: ServiceStatus[], opts: Opts = {}) {
	const { rs = aViewStore(aLiveRun()), workspace = true, error, onstage, visible = true } = opts;
	const data: RunServices = { workspace, error, services };
	rs.services = data;
	vi.mocked(api.runs.services).mockResolvedValue(data);
	const view = render(ServicesView, { rs, visible, onstage });
	await tick();
	return { ...view, rs };
}

describe('ServicesView', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		toasts.list = [];
		fleet.projects = [];
		confirmState.current = null;
		vi.mocked(api.runs.serviceLog).mockResolvedValue('');
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	describe('the feed', () => {
		it('holds the services feed while on screen, and lets go on hide', async () => {
			const rs = aViewStore(aLiveRun());
			const release = vi.fn();
			const hold = vi.spyOn(rs, 'useServices').mockReturnValue(release);

			const { rerender } = render(ServicesView, { rs, visible: true });
			expect(hold).toHaveBeenCalledOnce();

			await rerender({ visible: false });
			expect(release).toHaveBeenCalledOnce();
		});

		it('does not read while hidden', () => {
			render(ServicesView, { rs: aViewStore(aLiveRun()), visible: false });
			expect(api.runs.services).not.toHaveBeenCalled();
		});
	});

	describe('states', () => {
		it('says it is connecting until the first read', () => {
			vi.mocked(api.runs.services).mockReturnValue(deferred<RunServices>().promise);
			render(ServicesView, { rs: aViewStore(aLiveRun()), visible: true });
			expect(screen.getByRole('status')).toHaveTextContent('Connecting…');
		});

		it('offers a retry when the first read fails, and recovers', async () => {
			vi.mocked(api.runs.services).mockRejectedValue(new Error('engine down'));
			render(ServicesView, { rs: aViewStore(aLiveRun()), visible: true });

			const alert = await screen.findByRole('alert');
			expect(alert).toHaveTextContent('Services unavailable');
			expect(alert).toHaveTextContent('engine down');

			vi.mocked(api.runs.services).mockResolvedValue({
				workspace: true,
				services: [aService()]
			});
			await userEvent.click(within(alert).getByRole('button', { name: /Retry/ }));

			expect(await screen.findByText('app')).toBeInTheDocument();
			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		it('points at the settings when the workshop declares none', async () => {
			fleet.projects = [aProject({ id: 'p1', name: 'Payments' })];
			await open([], { rs: aViewStore(aLiveRun({ projectId: 'p1' })) });

			expect(screen.getByText('Nothing on the burner')).toBeInTheDocument();
			expect(screen.getByRole('link', { name: 'Payments › services' })).toHaveAttribute(
				'href',
				`${to.workshopSettings('p1')}#services`
			);
		});

		it('sums up how many are up', async () => {
			await open([
				aService({ name: 'app' }),
				aService({ name: 'db', state: 'starting' }),
				aService({ name: 'jobs', state: 'exited' })
			]);
			expect(screen.getByText('1 of 3 up')).toBeInTheDocument();
			expect(screen.getByText('refreshes every 3s')).toBeInTheDocument();
		});

		it('says what the workspace’s absence means', async () => {
			await open([aService({ state: 'stopped' })], { workspace: false });

			expect(screen.getByText(/^No workspace — nothing can start\./)).toBeInTheDocument();
			expect(screen.queryByText('refreshes every 3s')).not.toBeInTheDocument();
		});

		it('says when the workspace went', async () => {
			const rs = aViewStore(aRun({ state: 'completed' }), {
				now: NOW + 7_200_000 + 1000,
				events: [anEvent(1, 'env_destroyed')]
			});
			await open([aService({ state: 'stopped' })], { rs, workspace: false });

			expect(screen.getByText(/^The workspace went away 2h ago/)).toBeInTheDocument();
		});

		it('can start nothing and open no log without a workspace', async () => {
			await open([aService({ state: 'stopped' })], { workspace: false });

			expect(btn('Start')).toBeDisabled();
			expect(btn('Start').title).toBe('No workspace to start it in');
			expect(btn('Logs')).toBeDisabled();
		});

		it('still lets an open log close when the workspace goes', async () => {
			const { rs } = await open([aService()]);
			await userEvent.click(btn('Logs'));

			rs.services = { workspace: false, services: [aService()] };
			await tick();

			expect(btn('Hide logs')).toBeEnabled();
		});

		it('warns when the workspace could not be read, and retries', async () => {
			await open([aService({ state: 'unknown' })], { error: 'docker is away' });

			const alert = screen.getByRole('alert');
			expect(alert).toHaveTextContent('every state is unknown');
			expect(alert).toHaveTextContent('docker is away');

			await userEvent.click(within(alert).getByRole('button', { name: /Retry/ }));
			expect(api.runs.services).toHaveBeenCalledTimes(2);
		});

		it('keeps the settings link on the foot, plain without a workshop', async () => {
			await open([aService()], { rs: aViewStore(aLiveRun(), { projectName: 'Payments' }) });
			expect(screen.getByText(/Declared in Payments › services\.$/)).toBeInTheDocument();
			expect(screen.queryByRole('link')).not.toBeInTheDocument();
		});

		it('falls back to the workshop until a name is known', async () => {
			await open([aService()], { rs: aViewStore(aLiveRun({ projectId: 'p1' })) });
			expect(screen.getByRole('link', { name: 'the workshop › services' })).toBeInTheDocument();
		});
	});

	describe('a service’s state', () => {
		it.each<[Partial<ServiceStatus>, string, string]>([
			[{ state: 'healthy' }, 'healthy', 'tint-success'],
			[{ state: 'starting' }, 'starting…', 'tint-warning'],
			[{ state: 'stopping' }, 'stopping…', 'tint-warning'],
			[{ state: 'unhealthy' }, 'unhealthy', 'tint-danger'],
			[{ state: 'exited', exitCode: 137 }, 'exited · code 137', 'tint-danger'],
			[{ state: 'exited', exitCode: 0 }, 'exited · code 0', 'tint-muted'],
			[{ state: 'exited' }, 'exited', 'tint-muted'],
			[{ state: 'stopped', startedAt: AT }, 'stopped', 'tint-muted'],
			[{ state: 'stopped' }, 'never started', 'tint-muted'],
			[{ state: 'unknown' }, 'unknown', 'tint-muted']
		])('reads %o as "%s"', async (over, word, tint) => {
			await open([aService(over)]);
			expect(screen.getByText(word)).toHaveClass(tint);
		});

		it('says how long it has been healthy', async () => {
			await open([aService({ healthyAt: AT })]);
			expect(screen.getByText('healthy').title).toMatch(/^healthy since Oct 31, 2026/);
		});

		it.each(['stopped', 'exited', 'unknown'] as const)('offers only Start when %s', async (s) => {
			await open([aService({ state: s })]);
			expect(btn('Start')).toBeEnabled();
			expect(noBtn('Stop')).not.toBeInTheDocument();
			expect(noBtn(/Restart$/)).not.toBeInTheDocument();
		});

		it.each(['healthy', 'unhealthy'] as const)('offers Restart and Stop when %s', async (s) => {
			await open([aService({ state: s })]);
			expect(btn(/Restart$/)).toBeEnabled();
			expect(btn('Stop')).toBeEnabled();
			expect(noBtn('Start')).not.toBeInTheDocument();
		});

		it('can be stopped but not restarted while starting', async () => {
			await open([aService({ state: 'starting' })]);
			expect(btn(/Restart$/)).toBeDisabled();
			expect(btn('Stop')).toBeEnabled();
		});

		it('can be neither while stopping', async () => {
			await open([aService({ state: 'stopping' })]);
			expect(btn(/Restart$/)).toBeDisabled();
			expect(btn('Stop')).toBeDisabled();
		});

		it('cannot be stopped from a run with no workspace behind the list', async () => {
			await open([aService()], { workspace: false });
			expect(btn(/Restart$/)).toBeDisabled();
			expect(btn('Stop')).toBeDisabled();
		});
	});

	describe('its facts', () => {
		const started = { startedAt: HOUR_AGO, by: 'stage' as const, stageId: 'seed' };

		it('names the stage that started it, as a way to that stage', async () => {
			const onstage = vi.fn();
			await open([aService(started)], { onstage });

			await userEvent.click(btn('seed'));

			expect(onstage).toHaveBeenCalledExactlyOnceWith('seed');
			expect(screen.getByText(/stops when its chain ends/)).toBeInTheDocument();
			expect(screen.getByText('· 1h ago')).toBeInTheDocument();
		});

		it('names the stage plainly when there is nowhere to go', async () => {
			await open([aService(started)]);
			expect(screen.getByText('seed').tagName).toBe('SPAN');
		});

		it('names the watcher a stage belongs to', async () => {
			await open([aService({ ...started, phase: 'fix-ci' })]);
			expect(screen.getByText('seed').parentElement).toHaveTextContent(
				'by stage seed of watcher fix-ci'
			);
		});

		it('says when a person started it, and that it stays', async () => {
			await open([aService({ startedAt: HOUR_AGO, by: 'human' })]);
			expect(screen.getByText(/by a person, from here/)).toBeInTheDocument();
			expect(screen.getByText(/stays up until someone stops it/)).toBeInTheDocument();
		});

		it('drops how long it lasts once it is down, and says when it ended', async () => {
			await open([aService({ ...started, state: 'exited', endedAt: HOUR_AGO })]);
			expect(screen.queryByText(/stops when its chain ends/)).not.toBeInTheDocument();
			expect(screen.getByText('ended').nextElementSibling).toHaveTextContent('1h ago');
		});

		it('keeps an old end to itself once the service is back up', async () => {
			await open([aService({ endedAt: HOUR_AGO })]);
			expect(screen.queryByText('ended')).not.toBeInTheDocument();
		});

		it('hides who started it while nobody has', async () => {
			await open([aService({ state: 'stopped' })]);
			expect(screen.queryByText('started')).not.toBeInTheDocument();
		});

		it('says how the last probe went', async () => {
			await open([
				aService({ name: 'up', probe: 'tcp 5432', lastProbeAt: HOUR_AGO, lastProbeOk: true }),
				aService({ name: 'down', probe: 'tcp 5433', lastProbeAt: HOUR_AGO, lastProbeOk: false })
			]);
			expect(card('up').getByText('· passing · 1h ago')).toHaveClass('hue-success');
			expect(card('down').getByText('· failing · 1h ago')).toHaveClass('hue-danger');
		});

		it('says a probe has not answered yet, while it runs', async () => {
			await open([aService({ probe: 'tcp 5432' })]);
			expect(screen.getByText('· not answered yet')).toBeInTheDocument();
		});

		it('says nothing of a probe on a service that is down', async () => {
			await open([aService({ state: 'stopped', probe: 'tcp 5432' })]);
			expect(screen.getByText('tcp 5432')).toBeInTheDocument();
			expect(screen.queryByText('· not answered yet')).not.toBeInTheDocument();
		});

		it('says when no probe is declared', async () => {
			await open([aService()]);
			expect(screen.getByText('none — up as soon as it started')).toBeInTheDocument();
		});

		it('links a port once it has a preview', async () => {
			await open([
				aService({
					ports: [
						{ port: 3000, label: 'web', address: 'http://localhost:3000/', url: 'https://p.test/' }
					]
				})
			]);
			const link = screen.getByRole('link', { name: /:3000 web/ });
			expect(link).toHaveAttribute('href', 'https://p.test/');
			expect(link).toHaveAttribute('target', '_blank');
			expect(screen.getByText('http://localhost:3000/')).toBeInTheDocument();
		});

		it('calls an unlabelled preview "open"', async () => {
			await open([
				aService({
					ports: [{ port: 3000, address: 'http://localhost:3000/', url: 'https://p.test/' }]
				})
			]);
			expect(screen.getByRole('link', { name: /:3000 open/ })).toBeInTheDocument();
		});

		it('explains a port with no preview yet', async () => {
			await open([
				aService({
					state: 'starting',
					ports: [{ port: 3000, label: 'web', address: 'http://localhost:3000/' }]
				})
			]);
			expect(screen.getByText(':3000 web').title).toBe(
				'Its preview link appears once it is healthy'
			);
			expect(screen.queryByRole('link', { name: /:3000/ })).not.toBeInTheDocument();
		});

		it('copies the command that forwards each port, naming the workspace', async () => {
			const user = userEvent.setup();
			const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
			await open([
				aService({
					ports: [
						{ port: 3000, label: 'web', address: 'http://localhost:3000/', url: 'https://p.test/' },
						{ port: 5432, address: 'localhost:5432' }
					]
				})
			]);
			expect(btn('Copy apothecary forward env-1 3000')).toBeInTheDocument();
			await user.click(btn('Copy apothecary forward env-1 5432'));
			expect(write).toHaveBeenCalledExactlyOnceWith('apothecary forward env-1 5432');
		});

		it('names the run when it has no workspace id, and offers nothing without a workspace', async () => {
			const rs = aViewStore(aRun({ envId: undefined }));
			const ports = [{ port: 3000, address: 'http://localhost:3000/' }];
			const view = await open([aService({ ports })], { rs });
			expect(btn(`Copy apothecary forward ${RUN} 3000`)).toBeInTheDocument();
			view.unmount();

			await open([aService({ state: 'stopped', ports })], { workspace: false });
			expect(noBtn(/^Copy apothecary forward/)).not.toBeInTheDocument();
		});

		it('shows its description and its last error', async () => {
			await open([aService({ description: 'The web app', lastError: 'bind: in use' })]);
			expect(screen.getByText('The web app')).toBeInTheDocument();
			expect(screen.getByText('bind: in use')).toBeInTheDocument();
		});
	});

	describe('acting', () => {
		it('starts a service and shows what the call answered', async () => {
			vi.mocked(api.runs.startService).mockResolvedValue(aService({ state: 'starting' }));
			await open([aService({ state: 'stopped' })]);
			vi.mocked(api.runs.services).mockReturnValue(deferred<RunServices>().promise);

			await userEvent.click(btn('Start'));

			expect(api.runs.startService).toHaveBeenCalledExactlyOnceWith(RUN, 'app');
			expect(await screen.findByText('starting…')).toBeInTheDocument();
			expect(toasts.list[0]).toMatchObject({ text: 'app is starting', tone: 'ok' });
		});

		it('restarts a service', async () => {
			vi.mocked(api.runs.restartService).mockResolvedValue(aService({ state: 'starting' }));
			await open([aService()]);

			await userEvent.click(btn(/Restart$/));

			expect(api.runs.restartService).toHaveBeenCalledExactlyOnceWith(RUN, 'app');
			expect(toasts.list[0]).toMatchObject({ text: 'app is restarting', tone: 'ok' });
		});

		it('stops a service a person started without asking', async () => {
			vi.mocked(api.runs.stopService).mockResolvedValue(aService({ state: 'stopping' }));
			await open([aService({ by: 'human' })]);

			await userEvent.click(btn('Stop'));

			expect(confirmState.current).toBeNull();
			expect(api.runs.stopService).toHaveBeenCalledExactlyOnceWith(RUN, 'app');
			expect(toasts.list[0]).toMatchObject({ text: 'app stopped', tone: 'info' });
		});

		it('does not ask when a person restarted what a stage first started', async () => {
			vi.mocked(api.runs.stopService).mockResolvedValue(aService({ state: 'stopping' }));
			await open([aService({ by: 'human', stageId: 'seed' })]);

			await userEvent.click(btn('Stop'));

			expect(confirmState.current).toBeNull();
			expect(api.runs.stopService).toHaveBeenCalledOnce();
		});

		it('reads the list again after acting', async () => {
			vi.mocked(api.runs.stopService).mockResolvedValue(aService({ state: 'stopping' }));
			await open([aService()]);
			await userEvent.click(btn('Stop'));
			await vi.waitFor(() => expect(api.runs.services).toHaveBeenCalledTimes(2));
		});

		it('touches only the service it answered for', async () => {
			vi.mocked(api.runs.stopService).mockResolvedValue(
				aService({ name: 'db', state: 'stopping' })
			);
			await open([aService(), aService({ name: 'db' })]);
			vi.mocked(api.runs.services).mockReturnValue(deferred<RunServices>().promise);

			await userEvent.click(card('db').getByRole('button', { name: 'Stop' }));

			expect(await card('db').findByText('stopping…')).toBeInTheDocument();
			expect(card('app').getByText('healthy')).toBeInTheDocument();
		});

		it('holds a service’s buttons while it acts, leaving the others free', async () => {
			vi.mocked(api.runs.restartService).mockReturnValue(deferred<ServiceStatus>().promise);
			await open([aService(), aService({ name: 'db' })]);

			await userEvent.click(card('app').getByRole('button', { name: /Restart$/ }));

			const held = card('app').getByRole('button', { name: /Restart$/ });
			expect(held).toBeDisabled();
			expect(held).toHaveAttribute('aria-busy', 'true');
			expect(card('app').getByRole('button', { name: 'Stop' })).toBeDisabled();
			expect(card('db').getByRole('button', { name: /Restart$/ })).toBeEnabled();
		});

		it('says why the workspace refused, and frees the buttons', async () => {
			vi.mocked(api.runs.startService).mockRejectedValue(new Error('no such service'));
			await open([aService({ state: 'stopped' })]);

			await userEvent.click(btn('Start'));

			const alert = await screen.findByRole('alert');
			expect(alert).toHaveTextContent('The workspace refused that');
			expect(alert).toHaveTextContent('no such service');
			expect(toasts.list).toHaveLength(0);
			expect(btn('Start')).toBeEnabled();
		});

		it('dismisses the refusal', async () => {
			vi.mocked(api.runs.startService).mockRejectedValue(new Error('no such service'));
			await open([aService({ state: 'stopped' })]);
			await userEvent.click(btn('Start'));

			await userEvent.click(await screen.findByRole('button', { name: 'Dismiss' }));

			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		it('clears the last refusal on the next attempt', async () => {
			vi.mocked(api.runs.startService).mockRejectedValueOnce(new Error('no such service'));
			await open([aService({ state: 'stopped' })]);
			await userEvent.click(btn('Start'));
			await screen.findByRole('alert');

			vi.mocked(api.runs.startService).mockReturnValue(deferred<ServiceStatus>().promise);
			await userEvent.click(btn('Start'));

			expect(screen.queryByRole('alert')).not.toBeInTheDocument();
		});

		describe('stopping what a stage started', () => {
			const byStage = aService({ startedAt: AT, by: 'stage', stageId: 'seed' });

			it('asks first, saying what is at stake', async () => {
				await open([byStage]);
				await userEvent.click(btn('Stop'));

				expect(confirmState.current).toMatchObject({
					title: 'Stop app?',
					action: 'Stop',
					tone: 'warn'
				});
				expect(confirmState.current?.body).toContain('Stage <b>seed</b> started it');
				expect(api.runs.stopService).not.toHaveBeenCalled();
			});

			it('escapes the stage it names in the dialog', async () => {
				await open([{ ...byStage, stageId: '<i>seed</i>' }]);
				await userEvent.click(btn('Stop'));
				expect(confirmState.current?.body).not.toContain('<i>');
			});

			it('leaves it running when declined', async () => {
				await open([byStage]);
				await userEvent.click(btn('Stop'));

				confirmState.settle(false);
				await tick();

				expect(api.runs.stopService).not.toHaveBeenCalled();
				expect(btn('Stop')).toBeEnabled();
			});

			it('stops it when confirmed', async () => {
				vi.mocked(api.runs.stopService).mockResolvedValue(aService({ state: 'stopping' }));
				await open([byStage]);
				await userEvent.click(btn('Stop'));

				confirmState.settle(true);

				await vi.waitFor(() => expect(api.runs.stopService).toHaveBeenCalledOnce());
			});

			it('does not ask once the run is over: no chain still needs it', async () => {
				vi.mocked(api.runs.stopService).mockResolvedValue(aService({ state: 'stopping' }));
				await open([byStage], { rs: aViewStore(aLiveRun({ state: 'completed' })) });

				await userEvent.click(btn('Stop'));

				expect(confirmState.current).toBeNull();
				expect(api.runs.stopService).toHaveBeenCalledOnce();
			});
		});
	});

	describe('logs', () => {
		it('opens a service’s log tail', async () => {
			vi.mocked(api.runs.serviceLog).mockResolvedValue('listening on 3000');
			await open([aService()]);

			await userEvent.click(btn('Logs'));

			expect(api.runs.serviceLog).toHaveBeenCalledExactlyOnceWith(RUN, 'app', 200);
			expect(await screen.findByText('listening on 3000')).toBeInTheDocument();
			expect(screen.getByText('last 200 lines')).toBeInTheDocument();
			expect(btn('Hide logs')).toHaveAttribute('aria-expanded', 'true');
			expect(btn('Hide logs')).toHaveAttribute('aria-pressed', 'true');
		});

		it('says it is connecting until the log lands', async () => {
			vi.mocked(api.runs.serviceLog).mockReturnValue(deferred<string>().promise);
			await open([aService()]);

			await userEvent.click(btn('Logs'));

			expect(screen.getByRole('status')).toHaveTextContent('Connecting…');
			expect(btn('Reload the log')).toHaveAttribute('aria-busy', 'true');
		});

		it('says when there is no output yet', async () => {
			await open([aService()]);
			await userEvent.click(btn('Logs'));
			expect(await screen.findByText('No output yet.')).toBeInTheDocument();
		});

		it('says why a log could not be read', async () => {
			vi.mocked(api.runs.serviceLog).mockRejectedValue(new Error('gone'));
			await open([aService()]);
			await userEvent.click(btn('Logs'));
			expect(await screen.findByText('No log: gone')).toBeInTheDocument();
		});

		it('recovers on reload', async () => {
			vi.mocked(api.runs.serviceLog).mockRejectedValueOnce(new Error('gone'));
			await open([aService()]);
			await userEvent.click(btn('Logs'));
			await screen.findByText('No log: gone');

			vi.mocked(api.runs.serviceLog).mockResolvedValue('back');
			await userEvent.click(btn('Reload the log'));

			expect(await screen.findByText('back')).toBeInTheDocument();
		});

		it('says so when a reload asked for fails', async () => {
			vi.mocked(api.runs.serviceLog).mockResolvedValueOnce('old lines');
			await open([aService()]);
			await userEvent.click(btn('Logs'));
			await screen.findByText('old lines');

			vi.mocked(api.runs.serviceLog).mockRejectedValue(new Error('gone'));
			await userEvent.click(btn('Reload the log'));

			expect(await screen.findByText('No log: gone')).toBeInTheDocument();
		});

		it('keeps the old text on screen while reloading', async () => {
			vi.mocked(api.runs.serviceLog).mockResolvedValueOnce('old lines');
			await open([aService()]);
			await userEvent.click(btn('Logs'));
			await screen.findByText('old lines');

			vi.mocked(api.runs.serviceLog).mockReturnValue(deferred<string>().promise);
			await userEvent.click(btn('Reload the log'));

			expect(screen.getByText('old lines')).toBeInTheDocument();
		});

		it('closes without reading again', async () => {
			await open([aService()]);
			await userEvent.click(btn('Logs'));
			await userEvent.click(btn('Hide logs'));

			expect(btn('Logs')).toHaveAttribute('aria-expanded', 'false');
			expect(screen.queryByText('last 200 lines')).not.toBeInTheDocument();
			expect(api.runs.serviceLog).toHaveBeenCalledOnce();
		});

		it('stays closed when the read lands after it was hidden', async () => {
			const log = deferred<string>();
			vi.mocked(api.runs.serviceLog).mockReturnValue(log.promise);
			await open([aService()]);
			await userEvent.click(btn('Logs'));
			await userEvent.click(btn('Hide logs'));

			log.resolve('late lines');
			await log.promise;
			await tick();

			expect(screen.queryByText('late lines')).not.toBeInTheDocument();
			expect(btn('Logs')).toBeInTheDocument();
		});

		it('reads an open log again after an action', async () => {
			vi.mocked(api.runs.restartService).mockResolvedValue(aService());
			await open([aService()]);
			await userEvent.click(btn('Logs'));

			await userEvent.click(btn(/Restart$/));

			await vi.waitFor(() => expect(api.runs.serviceLog).toHaveBeenCalledTimes(2));
		});

		it('reads no log after an action when none is open', async () => {
			vi.mocked(api.runs.restartService).mockResolvedValue(aService());
			await open([aService()]);
			await userEvent.click(btn(/Restart$/));
			await vi.waitFor(() => expect(toasts.list).toHaveLength(1));
			expect(api.runs.serviceLog).not.toHaveBeenCalled();
		});

		describe('following', () => {
			beforeEach(() => {
				vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
			});

			async function withOpenLog(service = aService()) {
				const view = await open([service]);
				btn('Logs').click();
				await vi.advanceTimersByTimeAsync(0);
				vi.mocked(api.runs.serviceLog).mockClear();
				return view;
			}

			it('follows the open log of a running service every 3s', async () => {
				await withOpenLog();
				await vi.advanceTimersByTimeAsync(6000);
				expect(api.runs.serviceLog).toHaveBeenCalledTimes(2);
			});

			it('does not follow a service that is down', async () => {
				await withOpenLog(aService({ state: 'exited' }));
				await vi.advanceTimersByTimeAsync(6000);
				expect(api.runs.serviceLog).not.toHaveBeenCalled();
			});

			it('does not follow a closed log', async () => {
				await open([aService()]);
				await vi.advanceTimersByTimeAsync(6000);
				expect(api.runs.serviceLog).not.toHaveBeenCalled();
			});

			it('does not stack reads on a slow one', async () => {
				await withOpenLog();
				vi.mocked(api.runs.serviceLog).mockReturnValue(deferred<string>().promise);
				await vi.advanceTimersByTimeAsync(9000);
				expect(api.runs.serviceLog).toHaveBeenCalledOnce();
			});

			it('skips while the tab is in the background', async () => {
				await withOpenLog();
				vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
				await vi.advanceTimersByTimeAsync(6000);
				expect(api.runs.serviceLog).not.toHaveBeenCalled();
			});

			it('stops when the window hides', async () => {
				const { rerender } = await withOpenLog();
				await rerender({ visible: false });
				await vi.advanceTimersByTimeAsync(6000);
				expect(api.runs.serviceLog).not.toHaveBeenCalled();
			});

			it('keeps the lines when one read of a followed log fails', async () => {
				vi.mocked(api.runs.serviceLog).mockResolvedValue('listening on 3000');
				await withOpenLog();
				vi.mocked(api.runs.serviceLog).mockRejectedValue(new Error('blip'));

				await vi.advanceTimersByTimeAsync(3000);

				expect(screen.getByText('listening on 3000')).toBeInTheDocument();
			});
		});
	});
});
