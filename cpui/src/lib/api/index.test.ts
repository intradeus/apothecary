import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
	AnalyticsQuery,
	DashboardDraft,
	ProjectDraft,
	RunInput,
	ScorecardDraft,
	TicketDraft
} from '$lib/domain';
import * as gen from './generated/sdk.gen';
import { client } from './generated/client.gen';
import { onUnauthorized } from './client';
import {
	busyRunOf,
	isInUse,
	linkedTicketsOf,
	api,
	analytics,
	ask,
	auth,
	automations,
	credentials,
	egress,
	files,
	flows,
	integrations,
	notices,
	pings,
	projects,
	runs,
	skills,
	skillsets,
	system,
	tickets,
	users,
	workspace
} from './index';

vi.mock('./generated/sdk.gen');

type Sdk = typeof gen;
type SdkName = keyof Sdk;

const answer = (data: unknown) => ({
	data,
	error: undefined,
	response: { ok: true, status: 200, statusText: 'OK' }
});
const refused = (error: unknown, status = 409, statusText = 'Conflict') => ({
	data: undefined,
	error,
	response: { ok: false, status, statusText }
});

/** What the engine answers this SDK call with, for this test. */
const reply = (name: SdkName, value: unknown = answer(undefined)) => {
	const fn = vi.mocked(gen[name]) as unknown as ReturnType<typeof vi.fn>;
	fn.mockResolvedValue(value);
	return fn;
};

describe('a call that fails', () => {
	it('throws the engine’s own message', async () => {
		reply('getRun', refused({ error: 'run is gone' }));
		await expect(runs.get('r')).rejects.toThrow('run is gone');
	});

	it('throws a plain-text answer as it is', async () => {
		reply('getRun', refused('flat out no'));
		await expect(runs.get('r')).rejects.toThrow('flat out no');
	});

	it('falls back on the status for an answer with no message', async () => {
		reply('getRun', refused({}, 500, 'Internal Server Error'));
		await expect(runs.get('r')).rejects.toThrow('500 Internal Server Error');
	});

	it('says the engine is unreachable when there was no answer at all', async () => {
		reply('getRun', { data: undefined, error: {}, response: undefined });
		await expect(runs.get('r')).rejects.toThrow('the engine could not be reached');
	});

	it('treats a non-2xx with no error body as a failure', async () => {
		reply('abortRun', {
			data: undefined,
			error: undefined,
			response: { ok: false, status: 502, statusText: 'Bad Gateway' }
		});
		await expect(runs.abort('r')).rejects.toThrow('502 Bad Gateway');
	});

	it('throws for a call that returns nothing too', async () => {
		reply('abortRun', refused({ error: 'already over' }));
		await expect(runs.abort('r')).rejects.toThrow('already over');
	});
});

describe('a list the engine answers null', () => {
	it.each<[string, () => Promise<unknown[]>, SdkName]>([
		['runs', () => runs.list(), 'listRuns'],
		['phases', () => runs.phases('r'), 'getRunPhases'],
		['merge request status', () => runs.mrStatus('r'), 'getRunMrStatus'],
		['repos', () => workspace.repos('r'), 'getWorkspaceRepos'],
		['tree', () => workspace.tree('r'), 'getWorkspaceTree'],
		['denials', () => egress.denials('r'), 'listEgressDenials'],
		['run audit', () => egress.runAudit('r'), 'getRunEgressAudit'],
		['project audit', () => egress.projectAudit('p'), 'getProjectEgressAudit'],
		['pings', () => pings.list(), 'listPings'],
		['pending pings', () => pings.pending(), 'listPings'],
		['notices', () => notices.list(), 'listNotices'],
		['tickets', () => tickets.list(), 'listTickets'],
		['projects', () => projects.list(), 'listProjects'],
		['branches', () => projects.branches('p'), 'listProjectBranches'],
		['merge requests', () => projects.mergeRequests('p'), 'listProjectMrs'],
		['secrets', () => projects.secrets('p'), 'listProjectSecrets'],
		['providers', () => credentials.providers(), 'listProviders'],
		['credentials', () => credentials.list(), 'listCredentials'],
		['integrations', () => integrations.list(), 'listIntegrations'],
		['repos of one', () => integrations.repos('i'), 'listIntegrationRepos'],
		['models', () => integrations.models('i'), 'listIntegrationModels'],
		['source tickets', () => integrations.sourceTickets('i'), 'searchSourceTickets'],
		['all flows', () => flows.listAll(), 'listAllFlows'],
		['flows', () => flows.list('p'), 'listFlows'],
		['templates', () => flows.templates(), 'listFlowTemplates'],
		['bindings', () => flows.bindings('p'), 'listFlowBindings'],
		['versions', () => flows.versions('p', 'f'), 'listFlowVersions'],
		['kinds', () => system.kinds(), 'listKinds'],
		['actions', () => system.actions(), 'listActions'],
		['signals', () => system.signals(), 'listSignals'],
		['channels', () => system.channels(), 'listChannels'],
		['automations', () => automations.list('p'), 'listAutomations'],
		['tokens', () => auth.tokens(), 'listMyTokens'],
		['ssh keys', () => auth.sshKeys(), 'listMySshKeys'],
		['users', () => users.list(), 'listUsers'],
		['skills', () => skills.list(), 'listSkills'],
		['skillsets', () => skillsets.list(), 'listSkillsets'],
		['skillset versions', () => skillsets.versions('s'), 'listSkillsetVersions'],
		['dashboards', () => analytics.dashboards.list(), 'listDashboards'],
		['scorecards', () => analytics.scorecards.list(), 'listScorecards'],
		['dimension values', () => analytics.values('runs', 'flow_name'), 'analyticsValues'],
		['drilled runs', () => analytics.drill('runs', []), 'drillAnalytics']
	])('reads %s as none', async (_, call, name) => {
		reply(name, answer(null));
		await expect(call()).resolves.toEqual([]);
	});

	it.each<[string, () => Promise<unknown[]>, SdkName, unknown]>([
		['resources', () => runs.resources('r'), 'getRunResources', { samples: null }],
		[
			'ticket attachments',
			() => tickets.attachments('t'),
			'getTicketAttachments',
			{ attachments: null }
		],
		['uploads', () => files.upload([]), 'uploadAttachments', { attachments: null }],
		['egress defaults', () => egress.defaults(), 'getEgressDefaults', { domains: null }]
	])('reads %s nested in an object as none', async (_, call, name, body) => {
		reply(name, answer(body));
		await expect(call()).resolves.toEqual([]);
	});
});

describe('a call that may have nothing to say', () => {
	it.each<[string, () => Promise<unknown>, SdkName]>([
		['the workspace log', () => runs.workspaceLog('r'), 'getWorkspaceLog'],
		['the egress policy', () => egress.policy('r'), 'getRunEgressPolicy'],
		['the latest check', () => projects.latestEnvCheck('p'), 'getLatestEnvCheck'],
		['the build cache', () => projects.buildCache('p'), 'getBuildCache'],
		['a binding', () => flows.binding('p', 'f'), 'getFlowBinding']
	])('answers null for %s when the engine refuses', async (_, call, name) => {
		reply(name, refused({ error: 'not here' }, 404));
		await expect(call()).resolves.toBeNull();
	});

	it.each<[string, () => Promise<unknown>, SdkName]>([
		['the egress policy', () => egress.policy('r'), 'getRunEgressPolicy'],
		['the latest check', () => projects.latestEnvCheck('p'), 'getLatestEnvCheck'],
		['the build cache', () => projects.buildCache('p'), 'getBuildCache'],
		['a binding', () => flows.binding('p', 'f'), 'getFlowBinding']
	])('answers null for %s when the answer is empty', async (_, call, name) => {
		reply(name, answer(undefined));
		await expect(call()).resolves.toBeNull();
	});

	it('maps what it does get', async () => {
		reply('getRunEgressPolicy', answer({ mode: 'deny', domains: ['a'] }));
		await expect(egress.policy('r')).resolves.toEqual({
			mode: 'deny',
			domains: ['a'],
			blockedDomains: []
		});
		reply('getBuildCache', answer({ available: true, bytes: 2 }));
		await expect(projects.buildCache('p')).resolves.toEqual({ available: true, bytes: 2 });
		reply('getLatestEnvCheck', answer({ id: 'c', status: 'pass' }));
		await expect(projects.latestEnvCheck('p')).resolves.toMatchObject({ id: 'c', status: 'pass' });
		reply('getFlowBinding', answer({ flow_name: 'f', params: { a: '1' } }));
		await expect(flows.binding('p', 'f')).resolves.toMatchObject({ flowName: 'f' });
		reply('getWorkspaceLog', answer('log text'));
		await expect(runs.workspaceLog('r', 10)).resolves.toBe('log text');
	});
});

describe('what each call sends', () => {
	it.each<[string, () => Promise<unknown>, SdkName, unknown]>([
		['abort', () => runs.abort('r'), 'abortRun', { path: { id: 'r' } }],
		['pause', () => runs.pause('r'), 'pauseRun', { path: { id: 'r' } }],
		['play', () => runs.play('r'), 'playRun', { path: { id: 'r' } }],
		['archive', () => runs.archive('r'), 'archiveRun', { path: { id: 'r' } }],
		['retry', () => runs.retry('r'), 'retryRun', { path: { id: 'r' } }],
		['take over', () => runs.takeOver('r'), 'takeOverRun', { path: { id: 'r' } }],
		['release', () => runs.release('r'), 'releaseRun', { path: { id: 'r' } }],
		[
			'a rebuild',
			() => runs.rebuild('r'),
			'rebuildWorkspace',
			{ path: { id: 'r' }, body: { no_cache: false } }
		],
		[
			'a rebuild from scratch',
			() => runs.rebuild('r', true),
			'rebuildWorkspace',
			{ path: { id: 'r' }, body: { no_cache: true } }
		],
		[
			'a phase start, on no subject',
			() => runs.startPhase('r', 'w'),
			'startPhase',
			{ path: { id: 'r', phase: 'w' }, body: {} }
		],
		[
			'a phase start, on a subject',
			() => runs.startPhase('r', 'w', 'repo'),
			'startPhase',
			{ path: { id: 'r', phase: 'w' }, body: { subject: 'repo' } }
		],
		[
			'the Ask model',
			() => api.ask.setModel('r', 'm'),
			'setAskModel',
			{ path: { id: 'r' }, body: { model: 'm' } }
		],
		['a ticket removal', () => tickets.remove('t'), 'deleteTicket', { path: { id: 't' } }],
		['a project removal', () => projects.remove('p'), 'deleteProject', { path: { id: 'p' } }],
		[
			'a check’s cancel',
			() => projects.cancelEnvCheck('c'),
			'cancelEnvCheck',
			{ path: { id: 'c' } }
		],
		[
			'the cache’s clearing',
			() => projects.clearBuildCache('p'),
			'clearBuildCache',
			{ path: { id: 'p' } }
		],
		[
			'a secret’s removal',
			() => projects.deleteSecret('p', 's'),
			'deleteProjectSecret',
			{ path: { id: 'p', name: 's' } }
		],
		[
			'a credential’s removal',
			() => credentials.remove('c'),
			'deleteCredential',
			{ path: { id: 'c' } }
		],
		[
			'an integration’s removal',
			() => integrations.remove('i'),
			'deleteIntegration',
			{ path: { id: 'i' } }
		],
		[
			'a flow’s removal',
			() => flows.remove('p', 'f'),
			'deleteFlow',
			{ path: { id: 'p', name: 'f' } }
		],
		[
			'an unbinding',
			() => flows.unbind('p', 'f'),
			'deleteFlowBinding',
			{ path: { id: 'p', name: 'f' } }
		],
		[
			'a channel’s removal',
			() => system.removeChannel('c'),
			'deleteChannel',
			{ path: { id: 'c' } }
		],
		[
			'an automation’s removal',
			() => automations.remove('p', 'a'),
			'deleteAutomation',
			{ path: { id: 'p', aid: 'a' } }
		],
		[
			'a password change',
			() => auth.changePassword('old', 'new'),
			'changeMyPassword',
			{ body: { current: 'old', password: 'new' } }
		],
		['a token’s revoking', () => auth.revokeToken('t'), 'deleteMyToken', { path: { id: 't' } }],
		['an SSH key’s removal', () => auth.removeSshKey('k'), 'deleteMySshKey', { path: { id: 'k' } }],
		[
			'a password reset',
			() => users.resetPassword('u', 'p'),
			'resetUserPassword',
			{ path: { id: 'u' }, body: { password: 'p' } }
		],
		['a skill’s removal', () => skills.remove('s'), 'deleteSkill', { path: { id: 's' } }],
		['a skillset’s removal', () => skillsets.remove('s'), 'deleteSkillset', { path: { id: 's' } }],
		[
			'a dashboard’s removal',
			() => analytics.dashboards.remove('d'),
			'deleteDashboard',
			{ path: { id: 'd' } }
		],
		[
			'a scorecard’s removal',
			() => analytics.scorecards.remove('s'),
			'deleteScorecard',
			{ path: { id: 's' } }
		]
	])('sends %s', async (_, call, name, args) => {
		const fn = reply(name);
		await expect(call()).resolves.toBeUndefined();
		expect(fn).toHaveBeenCalledWith(args);
	});

	it('logs out with nothing to say', async () => {
		const fn = reply('logout');
		await auth.logout();
		expect(fn).toHaveBeenCalledWith();
	});

	it.each<[string, RunInput | undefined, unknown]>([
		['nothing', undefined, {}],
		[
			'blanks, left out',
			{ flow: '', goal: '', sourceBranch: '', attachmentIds: [], params: {} },
			{}
		],
		[
			'everything',
			{
				flow: 'f',
				goal: 'g',
				sourceBranch: 'main',
				attachmentIds: ['a'],
				params: { p: '1' },
				mr: { iid: 3, repo: 'r' },
				skillsets: [{ id: 's', seq: 2 }, { id: 't' }]
			},
			{
				flow: 'f',
				goal: 'g',
				source_branch: 'main',
				attachment_ids: ['a'],
				params: { p: '1' },
				mr: { iid: 3, repo: 'r' },
				skillsets: [{ id: 's', seq: 2 }, { id: 't' }]
			}
		],
		['a merge request of the one repository', { mr: { iid: 3 } }, { mr: { iid: 3 } }],
		// A rerun's [] clears the original's skillsets; absent keeps them.
		['no skillsets, on purpose', { skillsets: [] }, { skillsets: [] }]
	])('starts a run with %s', async (_, input, body) => {
		const start = reply('startTicketRun', answer({ run_id: 'new' }));
		await expect(tickets.startRun('t', input)).resolves.toBe('new');
		expect(start).toHaveBeenCalledWith({ path: { id: 't' }, body });
		const again = reply('rerunRun', answer({ run_id: 'newer' }));
		await expect(runs.rerun('r', input)).resolves.toBe('newer');
		expect(again).toHaveBeenCalledWith({ path: { id: 'r' }, body });
	});

	it('filters runs', async () => {
		const fn = reply('listRuns', answer([]));
		await runs.list({ project: 'p', ticket: 't' });
		expect(fn).toHaveBeenCalledWith({ query: { project: 'p', ticket: 't' } });
	});

	it('asks for a log’s tail, 200 lines or the 64k of a stage unless told', async () => {
		const service = reply('getRunServiceLogs', answer('out'));
		await expect(runs.serviceLog('r', 's')).resolves.toBe('out');
		expect(service).toHaveBeenCalledWith({ path: { id: 'r', name: 's' }, query: { tail: 200 } });
		const stage = reply('getStageLog', answer('out'));
		await expect(runs.stageLog('r', 's')).resolves.toBe('out');
		expect(stage).toHaveBeenCalledWith({ path: { id: 'r', stage: 's' }, query: { n: 65536 } });
	});

	it('answers a port’s URL, and a message’s id', async () => {
		reply('exposeRunPort', answer({ port: 80, url: 'u', extra: 1 }));
		await expect(runs.exposePort('r', 80)).resolves.toEqual({ port: 80, url: 'u' });
		reply('sendRunMessage', answer({ message_id: 'm' }));
		await expect(runs.sendMessage('r', 'hi')).resolves.toBe('m');
		reply('openEditor', answer({ url: 'e' }));
		await expect(runs.openEditor('r')).resolves.toBe('e');
	});

	it.each<[string, (id: string, name: string) => Promise<unknown>, SdkName]>([
		['start', runs.startService, 'startRunService'],
		['restart', runs.restartService, 'restartRunService'],
		['stop', runs.stopService, 'stopRunService']
	])('answers a service’s %s with its status', async (_, call, name) => {
		const fn = reply(name, answer({ name: 's', state: 'healthy' }));
		await expect(call('r', 's')).resolves.toMatchObject({ name: 's', state: 'healthy' });
		expect(fn).toHaveBeenCalledWith({ path: { id: 'r', name: 's' } });
	});

	describe('a ping’s answer', () => {
		it('sends a bare answer with nothing else', async () => {
			const fn = reply('answerPing');
			await pings.answer('p', {
				answer: 'approve',
				feedback: '',
				attachmentIds: [],
				review: [],
				curated: {}
			});
			expect(fn).toHaveBeenCalledWith({ path: { pid: 'p' }, body: { answer: 'approve' } });
		});

		it('sends what the person said, in the wire’s words', async () => {
			const fn = reply('answerPing');
			await pings.answer('p', {
				answer: 'accept',
				feedback: 'note',
				content: { a: 1 },
				attachmentIds: ['x'],
				review: [{ path: 'a', body: 'b', startLine: 2, attachmentIds: [] }],
				curated: { 'r.yaml': { items: [{ item: 'notes/0', keep: false }] } }
			});
			expect(fn).toHaveBeenCalledWith({
				path: { pid: 'p' },
				body: {
					answer: 'accept',
					feedback: 'note',
					content: { a: 1 },
					attachment_ids: ['x'],
					review: [{ path: 'a', body: 'b', start_line: 2, end_line: 2 }],
					curated: { 'r.yaml': { items: [{ item: 'notes/0', keep: false }] } }
				}
			});
		});

		it('lists the pending by default', async () => {
			const fn = reply('listPings', answer([]));
			await pings.list();
			expect(fn).toHaveBeenCalledWith({ query: { status: 'pending', limit: 100 } });
		});
	});

	describe('a ticket', () => {
		it('sends every field: the endpoint replaces the ticket', async () => {
			const fn = reply('createTicket', answer({ id: 't' }));
			const draft: TicketDraft = { title: 'T', goal: 'G', projectId: 'p' };
			await tickets.create(draft);
			expect(fn).toHaveBeenCalledWith({
				body: {
					title: 'T',
					goal: 'G',
					project_id: 'p',
					branch: '',
					change_type: '',
					attachment_ids: [],
					source_id: '',
					source_key: ''
				}
			});
		});

		it('turns a create into an import with a source', async () => {
			const fn = reply('updateTicket', answer({ id: 't' }));
			await tickets.update('t', {
				title: 'T',
				goal: 'G',
				projectId: 'p',
				branch: 'b',
				changeType: 'fix',
				attachmentIds: ['a'],
				sourceIntegrationId: 'i',
				sourceKey: 'K-1'
			});
			expect(fn).toHaveBeenCalledWith({
				path: { id: 't' },
				body: {
					title: 'T',
					goal: 'G',
					project_id: 'p',
					branch: 'b',
					change_type: 'fix',
					attachment_ids: ['a'],
					source_id: 'i',
					source_key: 'K-1'
				}
			});
		});
	});

	describe('a project', () => {
		const wireProject = {
			id: 'p',
			name: 'dev',
			repo_account_id: 'acc',
			provisioner_id: 'prov',
			harness_ids: ['h'],
			repo: { url: 'u', locator: 'l', default_branch: 'main', agents_md_path: 'AGENTS.md' },
			repos: [{ name: 'n', url: 'v', locator: 'm', default_branch: 'dev' }],
			commands: { test: 'make test' },
			workspace: {
				extra_domains: ['a'],
				denied_domains: ['b'],
				blocked_domains: ['c'],
				egress_allowed_cidrs: ['10.0.0.0/8'],
				egress_allowed_ports: [22],
				code_server_extensions: ['e'],
				services: [
					{
						name: 's',
						start: 'run',
						description: 'd',
						stop: 'halt',
						health: { http: '/h', tcp: 80, command: 'c', timeout: '1s', interval: '2s' },
						ports: [{ port: 80, label: 'l', path: '/p' }]
					}
				],
				browser: { enabled: true },
				docker: true,
				egress_mode: 'deny',
				editor: {
					theme: 't',
					font_size: 12,
					tab_size: 2,
					format_on_save: true,
					word_wrap: false,
					trust_workspace: true
				}
			},
			default_flow: 'builder',
			created_at: 'c',
			updated_at: 'u'
		};

		it('sends back everything it read: a save replaces the whole project', async () => {
			reply('getProject', answer(wireProject));
			const fn = reply('updateProject', answer(wireProject));
			const draft = await projects.get('p');
			await projects.update('p', draft as ProjectDraft);
			const { id: _id, created_at: _c, updated_at: _u, ...request } = wireProject;
			expect(fn).toHaveBeenCalledWith({ path: { id: 'p' }, body: request });
		});

		it('leaves a service’s empty optionals off the wire', async () => {
			const fn = reply('createProject', answer({ id: 'p' }));
			const draft = await (async () => {
				reply('getProject', answer(wireProject));
				return projects.get('p');
			})();
			draft.workspace.services = [{ name: 's', start: 'run', ports: [] }];
			await projects.create(draft as ProjectDraft);
			const body = fn.mock.calls[0][0].body;
			expect(body.workspace.services).toEqual([{ name: 's', start: 'run' }]);
		});

		it('leaves an unset egress mode out', async () => {
			const fn = reply('createProject', answer({ id: 'p' }));
			const draft = await (async () => {
				reply('getProject', answer({ ...wireProject, workspace: {} }));
				return projects.get('p');
			})();
			await projects.create(draft as ProjectDraft);
			expect(fn.mock.calls[0][0].body.workspace).not.toHaveProperty('egress_mode');
		});

		it('starts a run on a merge request, its number a path segment', async () => {
			const fn = reply('startMrRun', answer({ run_id: 'r', ticket_id: 't' }));
			await expect(
				projects.startRunOnMR('p', 12, { flow: 'f', repo: 'x', params: { a: '1' } })
			).resolves.toEqual({ runId: 'r', ticketId: 't' });
			expect(fn).toHaveBeenCalledWith({
				path: { id: 'p', iid: '12' },
				body: { flow: 'f', repo: 'x', params: { a: '1' } }
			});
			await projects.startRunOnMR('p', 12, { params: {} });
			expect(fn).toHaveBeenLastCalledWith({ path: { id: 'p', iid: '12' }, body: {} });
		});

		it('filters merge requests by repository only when told', async () => {
			const fn = reply('listProjectMrs', answer([]));
			await projects.mergeRequests('p');
			expect(fn).toHaveBeenLastCalledWith({ path: { id: 'p' }, query: undefined });
			await projects.mergeRequests('p', 'r');
			expect(fn).toHaveBeenLastCalledWith({ path: { id: 'p' }, query: { repo: 'r' } });
		});

		it('sets a secret and starts a check', async () => {
			const secret = reply('setProjectSecret', answer({ name: 's', updated_at: 'u' }));
			await expect(projects.setSecret('p', 's', 'v')).resolves.toEqual({
				name: 's',
				updatedAt: 'u'
			});
			expect(secret).toHaveBeenCalledWith({ path: { id: 'p', name: 's' }, body: { value: 'v' } });
			const check = reply('startEnvCheck', answer({ check_id: 'c' }));
			await expect(projects.startEnvCheck('p', true)).resolves.toBe('c');
			expect(check).toHaveBeenCalledWith({ path: { id: 'p' }, body: { keep: true } });
		});
	});

	describe('a flow', () => {
		it('sends a layout only when there is one', async () => {
			const save = reply('updateFlow', answer({ name: 'f' }));
			await flows.save('p', 'f', 'y');
			expect(save).toHaveBeenLastCalledWith({ path: { id: 'p', name: 'f' }, body: { yaml: 'y' } });
			await flows.save('p', 'f', 'y', { nodes: {} });
			expect(save).toHaveBeenLastCalledWith({
				path: { id: 'p', name: 'f' },
				body: { yaml: 'y', layout: { nodes: {} } }
			});
			const create = reply('createFlow', answer({ name: 'f' }));
			await flows.create('p', 'f', 'y');
			expect(create).toHaveBeenLastCalledWith({
				path: { id: 'p' },
				body: { name: 'f', yaml: 'y' }
			});
			await flows.create('p', 'f', 'y', { nodes: {} });
			expect(create).toHaveBeenLastCalledWith({
				path: { id: 'p' },
				body: { name: 'f', yaml: 'y', layout: { nodes: {} } }
			});
		});

		it('copies a flow or a template, renamed only when told', async () => {
			const copy = reply('copyFlow', answer({ name: 'f' }));
			await flows.copy('p', 'f', 'q');
			expect(copy).toHaveBeenLastCalledWith({
				path: { id: 'p', name: 'f' },
				body: { to_project_id: 'q' }
			});
			await flows.copy('p', 'f', 'q', 'g');
			expect(copy).toHaveBeenLastCalledWith({
				path: { id: 'p', name: 'f' },
				body: { to_project_id: 'q', name: 'g' }
			});
			const template = reply('copyFlowTemplate', answer({ name: 'f' }));
			await flows.copyTemplate('f', 'q', 'g');
			expect(template).toHaveBeenLastCalledWith({
				path: { name: 'f' },
				body: { to_project_id: 'q', name: 'g' }
			});
		});

		it('binds parameters and validates text', async () => {
			const bind = reply('putFlowBinding', answer({ flow_name: 'f', params: { a: '1' } }));
			await flows.bind('p', 'f', { a: '1' });
			expect(bind).toHaveBeenCalledWith({
				path: { id: 'p', name: 'f' },
				body: { params: { a: '1' } }
			});
			const validate = reply('validateFlow', answer({ ok: true }));
			await expect(flows.validate('y')).resolves.toMatchObject({ ok: true });
			expect(validate).toHaveBeenCalledWith({ body: { yaml: 'y' } });
		});

		it('fetches the schema, a version by its hash, a flow by its name', async () => {
			reply('getFlowSchema', answer({ type: 'object' }));
			await expect(flows.schema()).resolves.toEqual({ type: 'object' });
			const version = reply('getFlowVersion', answer({ hash: 'h' }));
			await flows.version('p', 'h');
			expect(version).toHaveBeenCalledWith({ path: { id: 'p', hash: 'h' } });
			const get = reply('getFlow', answer({ name: 'f' }));
			await flows.get('p', 'f');
			expect(get).toHaveBeenCalledWith({ path: { id: 'p', name: 'f' } });
		});
	});

	describe('integrations and credentials', () => {
		it('patches an integration: a name left out stays blank for the engine to keep', async () => {
			const fn = reply('updateIntegration', answer({ id: 'i' }));
			await integrations.update('i', { secrets: { a: 'b' } });
			expect(fn).toHaveBeenLastCalledWith({
				path: { id: 'i' },
				body: { name: '', secrets: { a: 'b' } }
			});
			await integrations.update('i', { name: 'n', config: { x: 1 } });
			expect(fn).toHaveBeenLastCalledWith({
				path: { id: 'i' },
				body: { name: 'n', config: { x: 1 } }
			});
		});

		it.each<[string, (id: string) => Promise<unknown>, SdkName]>([
			['an integration', integrations.test, 'testIntegration'],
			['a credential', credentials.test, 'testCredential']
		])('tests %s, warnings defaulting to none', async (_, call, name) => {
			reply(name, answer({ ok: false, error: 'bad' }));
			await expect(call('x')).resolves.toEqual({ ok: false, error: 'bad', warnings: [] });
		});

		it('searches a tracker with a default page', async () => {
			const fn = reply('searchSourceTickets', answer([]));
			await integrations.sourceTickets('i');
			expect(fn).toHaveBeenCalledWith({ path: { id: 'i' }, query: { q: '', limit: 25 } });
		});

		it('narrows providers to a harness only when told', async () => {
			const fn = reply('listProviders', answer([]));
			await credentials.providers();
			expect(fn).toHaveBeenLastCalledWith({ query: {} });
			await credentials.providers('codex');
			expect(fn).toHaveBeenLastCalledWith({ query: { harness: 'codex' } });
		});

		it('imports a credential file, reading what it refused', async () => {
			reply('importCredentials', answer({ created: null, rejected: null }));
			await expect(credentials.importFile('t')).resolves.toEqual({ created: [], rejected: [] });
		});

		it.each<[string, object, boolean]>([
			['still pending', { pending: true, credential: { id: 'c' } }, true],
			['no credential yet', {}, true],
			['done', { credential: { id: 'c', kind: 'oauth' } }, false]
		])('finishes a sign-in: %s', async (_, body, waits) => {
			reply('completeSignIn', answer(body));
			const out = await credentials.completeSignIn('s', 'code', 'name');
			expect(out === null).toBe(waits);
		});
	});

	describe('accounts', () => {
		it('signs in by password', async () => {
			const fn = reply('login', answer({ user: { id: 'u' }, methods: [] }));
			await auth.login('me', 'pw');
			expect(fn).toHaveBeenCalledWith({
				body: { method: 'password', login: 'me', proof: 'pw' }
			});
		});

		it('hands a new token’s secret back once', async () => {
			reply('createMyToken', answer({ token: { id: 't' }, secret: 's3' }));
			await expect(auth.createToken('n')).resolves.toMatchObject({
				token: { id: 't' },
				secret: 's3'
			});
		});

		it('leaves a blank key name for the engine to fill', async () => {
			const fn = reply('addMySshKey', answer({ id: 'k' }));
			await auth.addSshKey('ssh-ed25519 AAA', '');
			expect(fn).toHaveBeenCalledWith({
				body: { public_key: 'ssh-ed25519 AAA', name: undefined }
			});
		});

		it('creates a user with exactly what it was given', async () => {
			const fn = reply('createUser', answer({ id: 'u' }));
			const d = { handle: 'h', email: 'e', name: 'n', password: 'p', admin: false };
			await users.create(d);
			expect(fn).toHaveBeenCalledWith({ body: d });
		});
	});

	describe('skills', () => {
		it('sends a skill’s folder as base64, modes only when set', async () => {
			const fn = reply('createSkill', answer({ id: 's' }));
			await skills.create({
				name: 'n',
				files: [
					{ path: 'a', bytes: Uint8Array.of(77, 97, 110) },
					{ path: 'b', bytes: new Uint8Array(), mode: 493 }
				]
			});
			expect(fn).toHaveBeenCalledWith({
				body: {
					name: 'n',
					files: [
						{ path: 'a', content_b64: 'TWFu' },
						{ path: 'b', content_b64: '', mode: 493 }
					]
				}
			});
		});

		it('sends a skillset’s description only when it has one', async () => {
			const fn = reply('createSkillset', answer({ id: 's' }));
			await skillsets.create({ name: 'n', skillIds: ['a'] });
			expect(fn).toHaveBeenLastCalledWith({ body: { name: 'n', skill_ids: ['a'] } });
			await skillsets.create({ name: 'n', description: 'd', skillIds: [] });
			expect(fn).toHaveBeenLastCalledWith({
				body: { name: 'n', description: 'd', skill_ids: [] }
			});
		});

		it('imports from a URL, a path only when given', async () => {
			const fn = reply('importSkills', answer({}));
			await skills.import('u');
			expect(fn).toHaveBeenLastCalledWith({ body: { url: 'u' } });
			await skills.import('u', 'p');
			expect(fn).toHaveBeenLastCalledWith({ body: { url: 'u', path: 'p' } });
		});

		it('asks for a version by its number as a path segment', async () => {
			const fn = reply('getSkillsetVersion', answer({ skillset_id: 's' }));
			await skillsets.version('s', 0);
			expect(fn).toHaveBeenCalledWith({ path: { id: 's', seq: '0' } });
		});
	});

	describe('analytics', () => {
		const query: AnalyticsQuery = {
			grain: 'runs',
			measures: [{ field: 'cost_usd', agg: 'median' }, { field: 'success_rate' }],
			groupBy: ['flow_hash'],
			filters: [{ field: 'state', op: 'in', values: ['completed'] }],
			time: { from: '2026-01-01T00:00:00Z', bucket: 'week' }
		};
		const wireQuery = {
			grain: 'runs',
			measures: [{ field: 'cost_usd', agg: 'median' }, { field: 'success_rate' }],
			group_by: ['flow_hash'],
			filters: [{ field: 'state', op: 'in', values: ['completed'] }],
			time: { from: '2026-01-01T00:00:00Z', bucket: 'week' }
		};

		it('sends a query with only the parts it sets', async () => {
			const fn = reply('queryAnalytics', answer({ rows: null }));
			await analytics.query(query);
			expect(fn).toHaveBeenLastCalledWith({ body: wireQuery });
			await analytics.query({
				...query,
				measures: [],
				groupBy: [],
				filters: [],
				time: { to: '2026-02-01T00:00:00Z' },
				order: { by: 'score', desc: true },
				limit: 10,
				score: { scorecard: 'builtin-cheapest', baseline: ['a'] }
			});
			expect(fn).toHaveBeenLastCalledWith({
				body: {
					grain: 'runs',
					measures: [],
					group_by: [],
					filters: [],
					time: { to: '2026-02-01T00:00:00Z' },
					order: { by: 'score', desc: true },
					limit: 10,
					score: { scorecard: 'builtin-cheapest', baseline: ['a'] }
				}
			});
		});

		it('leaves an empty baseline off: min-max scoring', async () => {
			const fn = reply('queryAnalytics', answer({}));
			await analytics.query({ ...query, time: {}, score: { scorecard: 's', baseline: [] } });
			expect(fn.mock.lastCall?.[0].body.score).toEqual({ scorecard: 's' });
		});

		it('asks for a dimension’s values and the runs behind a row in one window', async () => {
			const values = reply('analyticsValues', answer([{ value: 'p1', label: 'Shop' }]));
			await expect(
				analytics.values('runs', 'project_id', { from: '2026-01-01T00:00:00Z' })
			).resolves.toEqual([{ value: 'p1', label: 'Shop' }]);
			expect(values).toHaveBeenCalledWith({
				body: { grain: 'runs', field: 'project_id', time: { from: '2026-01-01T00:00:00Z' } }
			});
			const drill = reply('drillAnalytics', answer([]));
			await analytics.drill('attempts', [{ field: 'model', op: 'eq', values: ['m'] }]);
			expect(drill).toHaveBeenCalledWith({
				body: {
					grain: 'attempts',
					filters: [{ field: 'model', op: 'eq', values: ['m'] }],
					time: {}
				}
			});
		});

		it('saves a dashboard with its panels’ queries on the wire', async () => {
			const draft: DashboardDraft = {
				name: 'Mine',
				filters: ['flow_name'],
				panels: [{ id: 'p', title: 'Cost', query, viz: 'bar', width: 6 }]
			};
			const wired = {
				name: 'Mine',
				filters: ['flow_name'],
				panels: [{ id: 'p', title: 'Cost', query: wireQuery, viz: 'bar', width: 6 }]
			};
			const create = reply('createDashboard', answer({ id: 'd', name: 'Mine' }));
			await expect(analytics.dashboards.create(draft)).resolves.toMatchObject({ id: 'd' });
			expect(create).toHaveBeenCalledWith({ body: wired });
			const update = reply('updateDashboard', answer({ id: 'd' }));
			await analytics.dashboards.update('d', draft);
			expect(update).toHaveBeenCalledWith({ path: { id: 'd' }, body: wired });
			const get = reply('getDashboard', answer({ id: 'd', panels: null }));
			await expect(analytics.dashboards.get('d')).resolves.toMatchObject({ panels: [] });
			expect(get).toHaveBeenCalledWith({ path: { id: 'd' } });
		});

		it('saves a scorecard’s terms', async () => {
			const draft: ScorecardDraft = {
				name: 'Cheap',
				terms: [
					{ measure: { field: 'cost_usd', agg: 'median' }, direction: 'lower', weight: 0.8 },
					{ measure: { field: 'success_rate' }, direction: 'higher', weight: 0.2 }
				]
			};
			const wired = {
				name: 'Cheap',
				terms: [
					{ measure: { field: 'cost_usd', agg: 'median' }, direction: 'lower', weight: 0.8 },
					{ measure: { field: 'success_rate' }, direction: 'higher', weight: 0.2 }
				]
			};
			const create = reply('createScorecard', answer({ id: 's', terms: wired.terms }));
			await expect(analytics.scorecards.create(draft)).resolves.toMatchObject({
				terms: draft.terms
			});
			expect(create).toHaveBeenCalledWith({ body: wired });
			const update = reply('updateScorecard', answer({ id: 's' }));
			await analytics.scorecards.update('s', draft);
			expect(update).toHaveBeenCalledWith({ path: { id: 's' }, body: wired });
			const get = reply('getScorecard', answer({ id: 's' }));
			await expect(analytics.scorecards.get('s')).resolves.toMatchObject({ id: 's', terms: [] });
			expect(get).toHaveBeenCalledWith({ path: { id: 's' } });
		});

		it('reads the catalog through the api aggregate', async () => {
			reply('getAnalyticsCatalog', answer({ grains: null }));
			await expect(api.analytics.catalog()).resolves.toEqual({ grains: [] });
		});
	});

	describe('automations', () => {
		const draft = { name: 'n', kind: 'cron', flow: 'f', enabled: true } as const;

		it('leaves what it does not say off the wire', async () => {
			const fn = reply('createAutomation', answer({ id: 'a' }));
			await automations.create('p', { ...draft, query: '', params: {} });
			expect(fn).toHaveBeenCalledWith({ path: { id: 'p' }, body: draft });
		});

		it('sends the rule it says', async () => {
			const fn = reply('updateAutomation', answer({ id: 'a' }));
			await automations.update('p', 'a', {
				...draft,
				sourceId: 's',
				query: 'q',
				schedule: '@daily',
				ticketId: 't',
				repo: 'r',
				params: { x: '1' }
			});
			expect(fn).toHaveBeenCalledWith({
				path: { id: 'p', aid: 'a' },
				body: {
					...draft,
					source_id: 's',
					query: 'q',
					schedule: '@daily',
					ticket_id: 't',
					repo: 'r',
					params: { x: '1' }
				}
			});
		});
	});

	describe('the system', () => {
		it('saves settings in the wire’s words and reads them back', async () => {
			const fn = reply('updateSettings', answer({ snapshots_after: '30d' }));
			const out = await system.saveSettings({
				destroyCompletedAfter: 'a',
				destroyAbandoned: 'b',
				orphanUploadsAfter: 'c',
				snapshotsAfter: 'd'
			});
			expect(fn).toHaveBeenCalledWith({
				body: {
					destroy_completed_after: 'a',
					destroy_abandoned: 'b',
					orphan_uploads_after: 'c',
					snapshots_after: 'd'
				}
			});
			expect(out.snapshotsAfter).toBe('30d');
		});

		it('saves no cap as -1', async () => {
			const fn = reply('setLimits', answer({ max_concurrent_runs: -1, max_environments: 2 }));
			await expect(
				system.setLimits({ maxConcurrentRuns: null, maxEnvironments: 2 })
			).resolves.toEqual({ maxConcurrentRuns: null, maxEnvironments: 2 });
			expect(fn).toHaveBeenCalledWith({
				body: { max_concurrent_runs: -1, max_environments: 2 }
			});
		});

		it('adds a channel, its config flat', async () => {
			const fn = reply('addChannel', answer({ id: 'c' }));
			await system.addChannel('slack', { url: 'u' });
			expect(fn).toHaveBeenCalledWith({ body: { kind: 'slack', url: 'u' } });
		});

		it('reads the Grafana link, empty when unset', async () => {
			reply('getObservability', answer({ grafana_url: 'https://g.example' }));
			await expect(system.observability()).resolves.toBe('https://g.example');
			reply('getObservability', answer({ grafana_url: '' }));
			await expect(system.observability()).resolves.toBe('');
		});
	});

	describe('egress', () => {
		it('says whether a rule stuck', async () => {
			reply('allowEgressHost', answer({ persisted: true }));
			await expect(egress.allow('r', 'h')).resolves.toBe(true);
			const fn = reply('setEgressRule', answer({ persisted: false }));
			await expect(egress.setRule('r', 'h', false)).resolves.toBe(false);
			expect(fn).toHaveBeenCalledWith({ path: { id: 'r' }, body: { host: 'h', allow: false } });
		});

		it('passes the audit’s filter on', async () => {
			const fn = reply('getRunEgressAudit', answer([]));
			await egress.runAudit('r', { verdict: 'deny', host: 'x', limit: 5 });
			expect(fn).toHaveBeenCalledWith({
				path: { id: 'r' },
				query: { verdict: 'deny', host: 'x', limit: 5 }
			});
		});
	});

	describe('the workspace', () => {
		it('addresses the primary checkout unless told otherwise', async () => {
			const tree = reply('getWorkspaceTree', answer([]));
			await workspace.tree('r');
			expect(tree).toHaveBeenLastCalledWith({ path: { id: 'r' }, query: { path: '', repo: '' } });
			await workspace.tree('r', 'src', 'other');
			expect(tree).toHaveBeenLastCalledWith({
				path: { id: 'r' },
				query: { path: 'src', repo: 'other' }
			});
		});

		it('reads a checkout, primary or not', async () => {
			reply('getWorkspaceRepos', answer([{ name: 'n', primary: true }, { name: 'm' }]));
			const repos = await workspace.repos('r');
			expect(repos.map((c) => c.primary)).toEqual([true, false]);
		});

		it('builds the terminal’s socket from the page’s origin', () => {
			expect(workspace.terminalUrl('r', 80, 24)).toBe(
				'ws://localhost:3000/api/runs/r/terminal?cols=80&rows=24&repo='
			);
			expect(workspace.terminalUrl('r', 80, 24, 'a b')).toContain('repo=a+b');
		});

		it('speaks wss on a secure page', () => {
			vi.stubGlobal('location', { protocol: 'https:', host: 'apo.test' });
			expect(workspace.terminalUrl('r', 1, 1)).toMatch(/^wss:\/\/apo\.test\//);
			vi.unstubAllGlobals();
		});
	});

	describe('files', () => {
		it('uploads a form of files, under one name', async () => {
			const fn = reply('uploadAttachments', answer({ attachments: [{ id: 'a' }] }));
			const list = [new File(['x'], 'a.txt'), new File(['y'], 'b.txt')];
			await expect(files.upload(list)).resolves.toMatchObject([{ id: 'a' }]);
			const body = fn.mock.calls[0][0].body as FormData;
			expect(body.getAll('files')).toHaveLength(2);
		});

		it.each([
			[files.attachmentUrl('a'), '/api/attachments/a'],
			[files.attachmentUrl('a', true), '/api/attachments/a?download=1'],
			[files.snapshotUrl('s'), '/api/snapshots/s'],
			[files.snapshotUrl('s', true), '/api/snapshots/s?download=1']
		])('links %s', (url, out) => {
			expect(url).toBe(out);
		});
	});
});

describe('the streams', () => {
	class FakeEventSource {
		static last: FakeEventSource;
		onmessage: ((m: { data: string }) => void) | null = null;
		onopen: (() => void) | null = null;
		onerror: (() => void) | null = null;
		listeners: Record<string, (m: { data: string }) => void> = {};
		closed = false;
		readyState = 1;
		static CLOSED = 2;
		constructor(public url: string) {
			FakeEventSource.last = this;
		}
		addEventListener(name: string, fn: (m: { data: string }) => void) {
			this.listeners[name] = fn;
		}
		close() {
			this.closed = true;
		}
		send(name: string, data: unknown) {
			this.listeners[name]({ data: JSON.stringify(data) });
		}
	}

	beforeEach(() => {
		vi.stubGlobal('EventSource', FakeEventSource);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	describe('a run’s events', () => {
		it('replays from the sequence asked for, each event mapped', () => {
			const onEvent = vi.fn();
			runs.events('r', 7, onEvent);
			const es = FakeEventSource.last;
			expect(es.url).toBe('/api/runs/r/events?after_seq=7');
			es.onmessage?.({
				data: JSON.stringify({ run_id: 'r', seq: 8, ts: 't', type: 'x', payload: { a: 1 } })
			});
			expect(onEvent).toHaveBeenCalledWith({
				runId: 'r',
				seq: 8,
				ts: 't',
				type: 'x',
				payload: { a: 1 }
			});
		});

		it('listens for deltas only when asked, dropping ones it does not know', () => {
			runs.events('r', 0, vi.fn());
			expect(FakeEventSource.last.listeners).not.toHaveProperty('delta');
			const onDelta = vi.fn();
			runs.events('r', 0, vi.fn(), onDelta);
			const es = FakeEventSource.last;
			es.send('delta', { kind: 'mystery' });
			expect(onDelta).not.toHaveBeenCalled();
			es.send('delta', { kind: 'message', text: 'hi', stage_id: 's' });
			expect(onDelta).toHaveBeenCalledWith(
				expect.objectContaining({ kind: 'message', text: 'hi', stageId: 's' })
			);
		});

		it('closes when unsubscribed', () => {
			const stop = runs.events('r', 0, vi.fn());
			stop();
			expect(FakeEventSource.last.closed).toBe(true);
		});

		describe('when the stream breaks', () => {
			const event = (seq: number) => ({
				data: JSON.stringify({ run_id: 'r', seq, ts: 't', type: 'x' })
			});
			/** What a non-200 does: the browser gives up on the source. */
			const refused = (es: FakeEventSource) => {
				es.readyState = FakeEventSource.CLOSED;
				es.onerror?.();
			};

			beforeEach(() => {
				vi.useFakeTimers();
			});
			afterEach(() => {
				vi.useRealTimers();
			});

			it('re-opens a refused stream from the last event seen', () => {
				const onEvent = vi.fn();
				runs.events('r', 0, onEvent);
				const first = FakeEventSource.last;
				first.onmessage?.(event(3));

				refused(first);
				vi.runOnlyPendingTimers();

				const second = FakeEventSource.last;
				expect(second).not.toBe(first);
				expect(second.url).toBe('/api/runs/r/events?after_seq=3');
				second.onmessage?.(event(4));
				expect(onEvent).toHaveBeenLastCalledWith(expect.objectContaining({ seq: 4 }));
			});

			it('keeps trying while it is refused', () => {
				runs.events('r', 5, vi.fn());
				const first = FakeEventSource.last;

				refused(first);
				vi.runOnlyPendingTimers();
				const second = FakeEventSource.last;
				refused(second);
				vi.runOnlyPendingTimers();

				expect(FakeEventSource.last).not.toBe(second);
				expect(FakeEventSource.last.url).toBe('/api/runs/r/events?after_seq=5');
			});

			it('leaves a dropped connection to the browser', () => {
				runs.events('r', 0, vi.fn());
				const es = FakeEventSource.last;

				es.readyState = 0; // CONNECTING: the browser is on it
				es.onerror?.();
				vi.runOnlyPendingTimers();

				expect(FakeEventSource.last).toBe(es);
			});

			it('stops trying once unsubscribed', () => {
				const stop = runs.events('r', 0, vi.fn());
				const es = FakeEventSource.last;

				refused(es);
				stop();
				vi.runOnlyPendingTimers();

				expect(FakeEventSource.last).toBe(es);
			});

			it('closes the re-opened stream, not the dead one, when unsubscribed', () => {
				const stop = runs.events('r', 0, vi.fn());
				refused(FakeEventSource.last);
				vi.runOnlyPendingTimers();

				stop();

				expect(FakeEventSource.last.closed).toBe(true);
			});
		});
	});

	describe('the Ask pane', () => {
		describe('when the engine refuses the stream', () => {
			beforeEach(() => {
				vi.useFakeTimers();
			});
			afterEach(() => {
				vi.useRealTimers();
			});

			it('re-opens it from the last message, and says so', () => {
				const onReconnect = vi.fn();
				api.ask.stream('r', vi.fn(), onReconnect);
				const first = FakeEventSource.last;
				first.onopen?.();
				first.onmessage?.({ data: JSON.stringify({ message: { seq: 7, role: 'answer' } }) });

				first.readyState = FakeEventSource.CLOSED;
				first.onerror?.();
				vi.runOnlyPendingTimers();

				const second = FakeEventSource.last;
				expect(second).not.toBe(first);
				expect(second.url).toBe('/api/runs/r/ask/stream?after_seq=7');
				second.onopen?.();
				expect(onReconnect).toHaveBeenCalledOnce();
			});

			it('stops once unsubscribed', () => {
				const stop = api.ask.stream('r', vi.fn());
				const es = FakeEventSource.last;

				es.readyState = FakeEventSource.CLOSED;
				es.onerror?.();
				stop();
				vi.runOnlyPendingTimers();

				expect(FakeEventSource.last).toBe(es);
			});
		});

		it('follows from the start by default', () => {
			api.ask.stream('r', vi.fn());
			expect(FakeEventSource.last.url).toBe('/api/runs/r/ask/stream?after_seq=0');
			api.ask.stream('r', vi.fn(), undefined, 4);
			expect(FakeEventSource.last.url).toBe('/api/runs/r/ask/stream?after_seq=4');
		});

		it('hands on messages, deltas and state as frames', () => {
			const onFrame = vi.fn();
			api.ask.stream('r', onFrame);
			const es = FakeEventSource.last;
			es.onmessage?.({
				data: JSON.stringify({ message: { seq: 1, id: 'm', role: 'answer', text: 't', at: 'a' } })
			});
			expect(onFrame).toHaveBeenLastCalledWith({
				kind: 'message',
				message: expect.objectContaining({ id: 'm', role: 'answer' })
			});
			es.send('delta', { text: 'x', tool: '' });
			expect(onFrame).toHaveBeenLastCalledWith({ kind: 'delta', text: 'x', tool: undefined });
			es.send('delta', { tool: 'read' });
			expect(onFrame).toHaveBeenLastCalledWith({ kind: 'delta', text: '', tool: 'read' });
			es.send('state', { busy: true, model: 'm' });
			expect(onFrame).toHaveBeenLastCalledWith({ kind: 'state', busy: true, model: 'm' });
			es.send('state', {});
			expect(onFrame).toHaveBeenLastCalledWith({ kind: 'state', busy: false, model: undefined });
		});

		it('ignores a keep-alive with no message', () => {
			const onFrame = vi.fn();
			api.ask.stream('r', onFrame);
			FakeEventSource.last.onmessage?.({ data: '{}' });
			expect(onFrame).not.toHaveBeenCalled();
		});

		it('says only when the stream opens again, not the first time', () => {
			const onReconnect = vi.fn();
			api.ask.stream('r', vi.fn(), onReconnect);
			const es = FakeEventSource.last;
			es.onopen?.();
			expect(onReconnect).not.toHaveBeenCalled();
			es.onopen?.();
			expect(onReconnect).toHaveBeenCalledOnce();
		});

		it('closes when unsubscribed', () => {
			const stop = api.ask.stream('r', vi.fn());
			stop();
			expect(FakeEventSource.last.closed).toBe(true);
		});
	});
});

describe('onUnauthorized', () => {
	const answering = (status: number) =>
		vi.stubGlobal(
			'fetch',
			vi.fn(async () => new Response('{}', { status }))
		);

	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it('tells the session store when the engine answers 401', async () => {
		const gone = vi.fn();
		onUnauthorized(gone);
		answering(401);
		await client.get({ baseUrl: 'http://engine.test', url: '/api/me' });
		expect(gone).toHaveBeenCalledOnce();
	});

	it('says nothing on any other refusal', async () => {
		const gone = vi.fn();
		onUnauthorized(gone);
		answering(403);
		await client.get({ baseUrl: 'http://engine.test', url: '/api/me' });
		expect(gone).not.toHaveBeenCalled();
	});
});

describe('ticket sync', () => {
	it('lists every ticket, done ones included', async () => {
		const fn = reply('listTickets', answer([]));
		await tickets.list();
		expect(fn).toHaveBeenCalledWith();
	});

	it('moves a ticket, labels it and comments on it through its tracker', async () => {
		const status = reply('setTicketStatus', answer({ id: 't' }));
		await tickets.setStatus('t', 's2');
		expect(status).toHaveBeenCalledWith({ path: { id: 't' }, body: { status_id: 's2' } });
		const labels = reply('setTicketLabels', answer({ id: 't' }));
		await tickets.setLabels('t', ['a', 'b']);
		expect(labels).toHaveBeenCalledWith({ path: { id: 't' }, body: { labels: ['a', 'b'] } });
		const add = reply('addTicketComment', answer(null));
		await expect(tickets.addComment('t', 'hi')).resolves.toEqual([]);
		expect(add).toHaveBeenCalledWith({ path: { id: 't' }, body: { body: 'hi' } });
	});

	it('refetches a tracker list only when asked', async () => {
		const fn = reply('listTrackerStatuses', answer({ statuses: null, warnings: null }));
		await expect(integrations.statuses('i')).resolves.toEqual({ statuses: [], warnings: [] });
		expect(fn).toHaveBeenLastCalledWith({ path: { id: 'i' }, query: undefined });
		await integrations.statuses('i', true);
		expect(fn).toHaveBeenLastCalledWith({ path: { id: 'i' }, query: { refresh: '1' } });
	});

	it('sends replacements for deleted rows only when there are some', async () => {
		const fn = reply('putTrackerTypes', answer({ types: [] }));
		await integrations.saveTypes('i', []);
		expect(fn).toHaveBeenLastCalledWith({ path: { id: 'i' }, body: { types: [] } });
		await integrations.saveTypes('i', [], { old: 'new' });
		expect(fn).toHaveBeenLastCalledWith({
			path: { id: 'i' },
			body: { types: [], replace_with: { old: 'new' } }
		});
	});

	it('unlinks the tickets of a source only when asked', async () => {
		const fn = reply('deleteIntegration');
		await integrations.remove('i', { unlinkTickets: true });
		expect(fn).toHaveBeenCalledWith({ path: { id: 'i' }, query: { unlink_tickets: '1' } });
	});

	it('names the run in the way of a start', async () => {
		reply('startTicketRun', refused({ error: 'busy', run_id: 'r9' }));
		const e = await tickets.startRun('t').catch((x: unknown) => x);
		expect(busyRunOf(e)).toBe('r9');
		expect(busyRunOf(new Error('busy'))).toBeUndefined();
	});

	it('counts what deleting a source would unlink', async () => {
		reply('deleteIntegration', refused({ error: 'linked', tickets: 4, active_runs: 2 }));
		const e = await integrations.remove('i').catch((x: unknown) => x);
		expect(linkedTicketsOf(e)).toEqual({ tickets: 4, activeRuns: 2 });
		expect(isInUse(e)).toBe(true);
	});

	it('reads anything but a 409 as no conflict', async () => {
		reply('putTrackerStatuses', refused({ error: 'bad', tickets: 1 }, 400, 'Bad Request'));
		const e = await integrations.saveStatuses('i', []).catch((x: unknown) => x);
		expect(isInUse(e)).toBe(false);
		expect(linkedTicketsOf(e)).toBeUndefined();
	});
});

describe('notices', () => {
	it('lists the caller’s notices in our shape', async () => {
		reply(
			'listNotices',
			answer([
				{
					id: 'n1',
					user_id: 'u',
					kind: 'channel_failing',
					title: 'Your slack channel is failing',
					count: 2,
					created_at: 'c',
					last_seen_at: 'l'
				}
			])
		);
		await expect(notices.list()).resolves.toEqual([
			{
				id: 'n1',
				kind: 'channel_failing',
				title: 'Your slack channel is failing',
				body: '',
				link: '',
				count: 2,
				createdAt: 'c',
				lastSeenAt: 'l'
			}
		]);
	});

	it('dismisses one by id', async () => {
		const fn = reply('dismissNotice');
		await notices.dismiss('n1');
		expect(fn).toHaveBeenCalledWith({ path: { id: 'n1' } });
	});

	it('dismisses them all', async () => {
		const fn = reply('dismissAllNotices');
		await notices.dismissAll();
		expect(fn).toHaveBeenCalled();
	});
});

// The thin reads and writes: each one reaches its endpoint with its arguments
// as the path, query or body the contract names, and maps what comes back
// without choking on a sparse answer.
describe('the rest of the surface', () => {
	type Case = [string, () => Promise<unknown>, SdkName, unknown, unknown];
	const cases: Case[] = [
		['runs.files', () => runs.files('r'), 'getRunFiles', answer({}), { path: { id: 'r' } }],
		['runs.ports', () => runs.ports('r'), 'getRunPorts', answer({}), { path: { id: 'r' } }],
		['runs.ssh', () => runs.ssh('r'), 'getRunSsh', answer({}), { path: { id: 'r' } }],
		[
			'runs.services',
			() => runs.services('r'),
			'getRunServices',
			answer({}),
			{ path: { id: 'r' } }
		],
		['ask.state', () => ask.state('r'), 'getAsk', answer({}), { path: { id: 'r' } }],
		[
			'ask.send',
			() => ask.send('r', 'why?'),
			'askRun',
			answer({}),
			{ path: { id: 'r' }, body: { text: 'why?' } }
		],
		[
			'workspace.file',
			() => workspace.file('r', 'a.ts'),
			'getWorkspaceFile',
			answer({}),
			{ path: { id: 'r' }, query: { path: 'a.ts', repo: '' } }
		],
		[
			'workspace.changes',
			() => workspace.changes('r', 'docs'),
			'getWorkspaceChanges',
			answer({}),
			{ path: { id: 'r' }, query: { repo: 'docs' } }
		],
		[
			'workspace.diff',
			() => workspace.diff('r', 'a.ts'),
			'getWorkspaceDiff',
			answer({}),
			{ path: { id: 'r' }, query: { path: 'a.ts', repo: '' } }
		],
		['pings.get', () => pings.get('p'), 'getPing', answer({}), { path: { pid: 'p' } }],
		['pings.payload', () => pings.payload('p'), 'getPingFiles', answer({}), { path: { pid: 'p' } }],
		[
			'pings.curation',
			() => pings.curation('p'),
			'getPingCuration',
			answer({}),
			{ path: { pid: 'p' } }
		],
		['tickets.get', () => tickets.get('t'), 'getTicket', answer({}), { path: { id: 't' } }],
		['tickets.sync', () => tickets.sync('t'), 'syncTicket', answer({}), { path: { id: 't' } }],
		[
			'projects.preflight',
			() => projects.preflight('p'),
			'preflightProject',
			answer({}),
			{ path: { id: 'p' } }
		],
		[
			'projects.envCheck',
			() => projects.envCheck('c'),
			'getEnvCheck',
			answer({}),
			{ path: { id: 'c' } }
		],
		[
			'projects.browser',
			() => projects.browser('p'),
			'getProjectBrowser',
			answer({}),
			{ path: { id: 'p' } }
		],
		[
			'credentials.addKey',
			() => credentials.addKey('acme', 'k', 'work'),
			'createCredential',
			answer({}),
			{ body: { provider: 'acme', key: 'k', name: 'work' } }
		],
		[
			'credentials.rename',
			() => credentials.rename('c', 'home'),
			'renameCredential',
			answer({}),
			{ path: { id: 'c' }, body: { name: 'home' } }
		],
		[
			'credentials.signIn',
			() => credentials.signIn('acme'),
			'startSignIn',
			answer({}),
			{ body: { provider: 'acme' } }
		],
		[
			'integrations.sourceTicket',
			() => integrations.sourceTicket('i', 'K-1'),
			'getSourceTicket',
			answer({}),
			{ path: { id: 'i', key: 'K-1' } }
		],
		['files.limits', () => files.limits(), 'getAttachmentLimits', answer({}), undefined],
		[
			'system.harnessOptions',
			() => system.harnessOptions(),
			'listHarnessOptions',
			answer([]),
			undefined
		],
		['system.settings', () => system.settings(), 'getSettings', answer({}), undefined],
		[
			'auth.setup',
			() => auth.setup({ handle: 'lu', email: 'lu@x.test', name: 'Lu', password: 'longenough' }),
			'setupInstance',
			answer({ user: {} }),
			{ body: { handle: 'lu', email: 'lu@x.test', name: 'Lu', password: 'longenough' } }
		],
		['auth.me', () => auth.me(), 'getMe', answer({ user: {} }), undefined],
		[
			'auth.updateMe',
			() => auth.updateMe({ name: 'Lu' }),
			'updateMe',
			answer({ user: {} }),
			{ body: { name: 'Lu' } }
		],
		[
			'users.update',
			() => users.update('u', { admin: true }),
			'updateUser',
			answer({}),
			{ path: { id: 'u' }, body: { admin: true } }
		],
		['skills.get', () => skills.get('s'), 'getSkill', answer({ skill: {} }), { path: { id: 's' } }],
		[
			'skills.update',
			() => skills.update('s', { name: 'n', files: [] }),
			'updateSkill',
			answer({}),
			{ path: { id: 's' }, body: { name: 'n', files: [] } }
		],
		[
			'skills.refresh',
			() => skills.refresh('s'),
			'refreshSkill',
			answer({}),
			{ path: { id: 's' } }
		],
		['skillsets.get', () => skillsets.get('k'), 'getSkillset', answer({}), { path: { id: 'k' } }],
		[
			'skillsets.update',
			() => skillsets.update('k', { name: 'n', skillIds: ['s'] }),
			'updateSkillset',
			answer({}),
			{ path: { id: 'k' }, body: { name: 'n', skill_ids: ['s'] } }
		],
		[
			'skillsets.refresh',
			() => skillsets.refresh('k'),
			'refreshSkillset',
			answer({ skillset: {} }),
			{ path: { id: 'k' } }
		]
	];

	it.each(cases)('%s', async (_name, call, sdk, wire, args) => {
		const fn = reply(sdk, wire);
		await call();
		if (args === undefined) expect(fn).toHaveBeenCalledOnce();
		else expect(fn).toHaveBeenCalledWith(args);
	});

	it('reports a tracker sync in our words', async () => {
		reply(
			'syncIntegration',
			answer({ tickets: 3, synced: 2, gone: 0, failed: 1, busy: false, rate_limited: true })
		);
		expect(await integrations.sync('i')).toEqual({
			tickets: 3,
			synced: 2,
			gone: 0,
			failed: 1,
			busy: false,
			rateLimited: true
		});
	});

	it('asks a tracker for its types, refreshed only when told', async () => {
		const fn = reply('listTrackerTypes', answer({ types: [] }));
		expect(await integrations.types('i')).toEqual([]);
		expect(fn).toHaveBeenLastCalledWith({ path: { id: 'i' }, query: undefined });
		await integrations.types('i', true);
		expect(fn).toHaveBeenLastCalledWith({ path: { id: 'i' }, query: { refresh: '1' } });
	});

	it('reads a negative cap as no cap', async () => {
		reply('getLimits', answer({ max_concurrent_runs: -1, max_environments: 2 }));
		expect(await system.limits()).toEqual({ maxConcurrentRuns: null, maxEnvironments: 2 });
	});
});
