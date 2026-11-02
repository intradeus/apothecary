import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { PingLike } from '$lib/core/ping';
import { aPing } from '$test/fixtures';
import { aFile, aForm, options } from '$test/fixtures/run.svelte';
import { deferred } from '$test/helpers';
import PingCard from './PingCard.svelte';

vi.mock('$lib/api', () => ({
	api: {
		files: { upload: vi.fn(), limits: vi.fn(async () => null) },
		runs: { ssh: vi.fn(async () => ({ enabled: false })), openEditor: vi.fn() }
	}
}));

const NOW = Date.parse('2026-10-31T12:00:00Z');
const FIVE_MIN_AGO = '2026-10-31T11:55:00Z';

function setup(over: Partial<PingLike> = {}, props: Record<string, unknown> = {}) {
	const onanswer = vi.fn();
	const view = render(PingCard, {
		ping: aPing(over as Parameters<typeof aPing>[0]) as PingLike,
		onanswer,
		...props
	});
	return { onanswer, ...view };
}

const question = (over: Partial<PingLike> = {}): Partial<PingLike> => ({
	type: 'clarification',
	answerKind: 'form',
	question: 'Which one?',
	...over
});

const btn = (name: string | RegExp) => screen.getByRole('button', { name });
const noBtn = (name: string | RegExp) => screen.queryByRole('button', { name });
const box = (name: string | RegExp) => screen.getByRole('textbox', { name });

const FIELD_NOTE = /what should change/i;
const REASON = /why none of this fits/i;

async function attach(container: HTMLElement, ...files: File[]) {
	await userEvent.click(btn(/attach/i));
	await userEvent.upload(container.querySelector('input[type="file"]') as HTMLInputElement, files);
}

beforeEach(() => {
	vi.clearAllMocks();
	vi.useFakeTimers({ toFake: ['Date'] });
	vi.setSystemTime(NOW);
});

afterEach(() => {
	vi.useRealTimers();
});

describe('an answered ping', () => {
	const answered = { status: 'answered', answer: 'approve', stageId: 'plan' };

	it('collapses to one line with no way to answer again', () => {
		setup(answered);
		expect(screen.getByText('Gate')).toBeInTheDocument();
		expect(screen.queryByText('Approve the plan?')).not.toBeInTheDocument();
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});

	it('says when and what', () => {
		setup({ ...answered, answeredAt: FIVE_MIN_AGO });
		expect(screen.getByText(/answered 5m ago —/)).toBeInTheDocument();
		expect(screen.getByText('approve')).toBeInTheDocument();
	});

	it('still reads with no time on it', () => {
		setup(answered);
		expect(screen.getByText(/answered —/)).toBeInTheDocument();
	});

	it('shows the feedback that went with it', () => {
		setup({ ...answered, answer: 'reject', feedback: 'Too broad' });
		expect(screen.getByTitle('Too broad')).toHaveTextContent('Too broad');
	});

	it("reads a form's answer in the option's words", () => {
		const form = aForm({ id: 'env', options: [{ id: 'p', label: 'Production' }] });
		setup(question({ status: 'answered', answer: 'accept', content: { env: 'p' }, form }));
		expect(screen.getByText('Production')).toBeInTheDocument();
	});

	it('reads a declined form as declined', () => {
		const form = aForm({ id: 'env' });
		setup(question({ status: 'answered', answer: 'decline', feedback: 'Neither', form }));
		expect(screen.getByText('Declined')).toBeInTheDocument();
	});
});

describe('a withdrawn ping', () => {
	it('says so, with no way to answer', () => {
		setup({ status: 'withdrawn', stageId: 'plan' });
		expect(screen.getByText('withdrawn')).toBeInTheDocument();
		expect(screen.getByText('plan')).toBeInTheDocument();
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});
});

describe('the head', () => {
	it('reads a ping with no status as pending', () => {
		setup({ status: undefined });
		expect(btn('Approve')).toBeEnabled();
	});

	it.each([
		['gate', 'the bench awaits your seal'],
		['debug', 'the bench is yours — mend it, then continue'],
		['clarification', 'a note from the agent'],
		['blocker', 'a note from the agent'],
		['permission', 'a note from the agent']
	])('words a %s', (type, aside) => {
		setup({ type });
		expect(screen.getByText(aside)).toBeInTheDocument();
	});

	it('marks only what an agent raised as unwanted', () => {
		const { unmount } = setup({ type: 'blocker' });
		expect(screen.getByText('unwanted · counts against run')).toBeInTheDocument();
		unmount();
		setup({ type: 'gate' });
		expect(screen.queryByText('unwanted · counts against run')).not.toBeInTheDocument();
	});

	it('names the stage and when it was raised', () => {
		setup({ stageId: 'plan', raisedAt: FIVE_MIN_AGO });
		expect(screen.getByText('plan')).toBeInTheDocument();
		expect(screen.getByText('raised 5m ago')).toBeInTheDocument();
	});

	it('takes the stage prop over the ping’s own', () => {
		setup({ stageId: 'plan' }, { stage: 'review' });
		expect(screen.getByText('review')).toBeInTheDocument();
		expect(screen.queryByText('plan')).not.toBeInTheDocument();
	});

	it('links the run title to the run page', () => {
		setup({ runId: 'r1' } as Partial<PingLike>, { runTitle: 'My run' });
		expect(screen.getByRole('link', { name: 'My run' })).toHaveAttribute('href', '/runs/r1');
	});

	it('takes a link it is given over the run page', () => {
		setup({ runId: 'r1' } as Partial<PingLike>, { runTitle: 'My run', runHref: '/elsewhere' });
		expect(screen.getByRole('link', { name: 'My run' })).toHaveAttribute('href', '/elsewhere');
	});

	it('shows a plain title when it cannot link', () => {
		setup({ runId: undefined } as Partial<PingLike>, { runTitle: 'My run' });
		expect(screen.getByText('My run')).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: 'My run' })).not.toBeInTheDocument();
	});

	it('shows the ticket beside the run', () => {
		setup({}, { runTitle: 'My run', ticketTitle: 'The ticket', ticketKey: 'ABC-1' });
		expect(screen.getByText('The ticket')).toBeInTheDocument();
		expect(screen.getByText('ABC-1')).toBeInTheDocument();
	});

	it('does not repeat a ticket titled like the run', () => {
		setup({}, { runTitle: 'Same', ticketTitle: 'Same' });
		expect(screen.getAllByText('Same')).toHaveLength(1);
		expect(screen.queryByText('·')).not.toBeInTheDocument();
	});
});

describe('the body', () => {
	it('renders the question and its context as markdown', () => {
		setup({ question: 'Ship **this**?', context: 'See `notes`' });
		expect(screen.getByText('this').tagName).toBe('STRONG');
		expect(screen.getByText('notes').tagName).toBe('CODE');
	});

	it('links out to a link payload in a new tab', () => {
		setup({ link: 'https://git.test/mr/1' });
		const link = screen.getByRole('link', { name: /git\.test\/mr\/1/ });
		expect(link).toHaveAttribute('href', 'https://git.test/mr/1');
		expect(link).toHaveAttribute('target', '_blank');
		expect(link).toHaveAttribute('rel', 'noreferrer');
	});

	it('sends a shown file to the review workbench', () => {
		setup({ id: 'p1', files: ['docs/plan.md'] });
		expect(screen.getByText('docs/plan.md')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: /review & comment/i })).toHaveAttribute(
			'href',
			'/inbox/p1'
		);
	});

	it('counts the other files', () => {
		setup({ files: ['a.md', 'b.md', 'c.md'] });
		expect(screen.getByText('+ 2 more files')).toBeInTheDocument();
	});

	it('says file, singular, for one more', () => {
		setup({ files: ['a.md', 'b.md'] });
		expect(screen.getByText('+ 1 more file')).toBeInTheDocument();
	});

	it('shows a link and no review for a ping with only a link', () => {
		setup({ link: 'https://x.test', primaryRef: 'https://x.test' });
		expect(screen.queryByRole('link', { name: /review/i })).not.toBeInTheDocument();
	});

	describe('a review document at a gate', () => {
		const review = {
			id: 'p1',
			files: ['review.yaml'],
			fileTypes: { 'review.yaml': 'review_threads' }
		};

		it('offers curating first, and approving only as written', async () => {
			const { onanswer } = setup(review);
			expect(screen.getByRole('link', { name: /curate & post/i })).toHaveAttribute(
				'href',
				'/inbox/p1'
			);
			await userEvent.click(btn('Approve as written'));
			expect(onanswer.mock.calls[0][0]).toBe('approve');
		});

		it('reads a file of another type as a plain review', () => {
			setup({ files: ['threads.yaml'], fileTypes: { 'threads.yaml': 'mr_threads' } });
			expect(screen.getByRole('link', { name: /review & comment/i })).toBeInTheDocument();
			expect(btn('Approve')).toBeInTheDocument();
		});
	});
});

describe('a yes or no', () => {
	it('approves with nothing else attached', async () => {
		const { onanswer } = setup();
		await userEvent.click(btn('Approve'));
		expect(onanswer).toHaveBeenCalledExactlyOnceWith('approve', '', undefined, undefined);
	});

	it('keeps the feedback box shut until asked', async () => {
		setup();
		expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
		await userEvent.click(btn('Request changes…'));
		expect(box(FIELD_NOTE)).toBeInTheDocument();
	});

	it('needs a reason before it sends changes', async () => {
		const { onanswer } = setup();
		await userEvent.click(btn('Request changes…'));
		expect(btn('Send changes')).toBeDisabled();

		await userEvent.type(box(FIELD_NOTE), '   ');
		expect(btn('Send changes')).toBeDisabled();

		await userEvent.type(box(FIELD_NOTE), 'Fix it');
		await userEvent.click(btn('Send changes'));
		expect(onanswer).toHaveBeenCalledExactlyOnceWith('reject', '   Fix it', undefined, undefined);
	});

	it.each(['{Control>}{Enter}{/Control}', '{Meta>}{Enter}{/Meta}'])(
		'sends changes on %s',
		async (keys) => {
			const { onanswer } = setup();
			await userEvent.click(btn('Request changes…'));
			await userEvent.type(box(FIELD_NOTE), `Fix it${keys}`);
			expect(onanswer).toHaveBeenCalledExactlyOnceWith('reject', 'Fix it', undefined, undefined);
		}
	);

	it('lets a plain Enter make a new line', async () => {
		const { onanswer } = setup();
		await userEvent.click(btn('Request changes…'));
		await userEvent.type(box(FIELD_NOTE), 'One{Enter}two');
		expect(onanswer).not.toHaveBeenCalled();
		expect(box(FIELD_NOTE)).toHaveValue('One\ntwo');
	});

	it('does not send an empty reason from the keyboard', async () => {
		const { onanswer } = setup();
		await userEvent.click(btn('Request changes…'));
		await userEvent.type(box(FIELD_NOTE), '{Control>}{Enter}{/Control}');
		expect(onanswer).not.toHaveBeenCalled();
	});

	it('does not send from the keyboard while busy', async () => {
		const { onanswer, rerender } = setup();
		await userEvent.click(btn('Request changes…'));
		await userEvent.type(box(FIELD_NOTE), 'Fix it');
		await rerender({ busy: true });
		await userEvent.type(box(FIELD_NOTE), '{Control>}{Enter}{/Control}');
		expect(onanswer).not.toHaveBeenCalled();
	});

	it('closes the box on Cancel', async () => {
		setup();
		await userEvent.click(btn('Request changes…'));
		await userEvent.click(btn('Cancel'));
		expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
		expect(btn('Request changes…')).toBeInTheDocument();
	});

	it('lets an open note ride along on an approval', async () => {
		const { onanswer } = setup();
		await userEvent.click(btn('Request changes…'));
		await userEvent.type(box(FIELD_NOTE), 'Small nit');
		await userEvent.click(btn('Approve'));
		expect(onanswer).toHaveBeenCalledWith('approve', 'Small nit', undefined, undefined);
	});

	it('does not send a cancelled note with the approval', async () => {
		const { onanswer } = setup();
		await userEvent.click(btn('Request changes…'));
		await userEvent.type(box(FIELD_NOTE), 'Actually this is wrong');
		await userEvent.click(btn('Cancel'));
		await userEvent.click(btn('Approve'));
		expect(onanswer).toHaveBeenCalledWith('approve', '', undefined, undefined);
	});
});

describe('a phase confirmation', () => {
	const confirm = { type: 'phase_confirm', question: 'Run review?' };

	it('reads as run it or not now, with what not now means', () => {
		setup(confirm);
		expect(btn('Run it')).toBeEnabled();
		expect(btn('Not now')).toBeEnabled();
		expect(
			screen.getByText('Not now: it is offered again only when what it reacts to changes.')
		).toBeInTheDocument();
		expect(noBtn('Approve')).not.toBeInTheDocument();
	});

	it.each([
		['Run it', 'approve'],
		['Not now', 'reject']
	])('%s answers %s, no reason needed', async (name, answer) => {
		const { onanswer } = setup(confirm);
		await userEvent.click(btn(name));
		expect(onanswer).toHaveBeenCalledExactlyOnceWith(answer, '', undefined, undefined);
	});
});

describe('a parked stage', () => {
	const debug = { type: 'debug', stageId: 'build', question: 'ignored' };

	it('states the failure itself, with the output verbatim', () => {
		const { container } = setup({ ...debug, context: 'exit 1\n**not bold**' });
		expect(container).toHaveTextContent('Stage build failed — check why.');
		expect(screen.queryByText('ignored')).not.toBeInTheDocument();
		expect(screen.getByText(/\*\*not bold\*\*/).tagName).toBe('PRE');
	});

	it('escapes a stage name in the wording it builds as html', () => {
		const { container } = setup({ ...debug, stageId: '<img src=x onerror=y>' });
		expect(container.querySelector('img')).toBeNull();
	});

	it('continues by re-running the stage', async () => {
		const { onanswer } = setup(debug);
		await userEvent.click(btn('Continue — re-run build'));
		expect(onanswer.mock.calls[0][0]).toBe('approve');
	});

	it('abandons the run, with a reason, when the run ends with it', async () => {
		const { onanswer } = setup(debug);
		await userEvent.click(btn('Abandon…'));
		const reason = box(/recorded as the reason the run was abandoned/i);
		expect(btn('Abandon the run')).toBeDisabled();

		await userEvent.type(reason, 'Hopeless');
		await userEvent.click(btn('Abandon the run'));

		expect(onanswer).toHaveBeenCalledExactlyOnceWith('reject', 'Hopeless', undefined, undefined);
	});

	it("ends only the round at a watcher's leaf, and says so", async () => {
		const { onanswer } = setup({ ...debug, endsPhase: true, phase: 'review' });
		await userEvent.click(btn('Give up this round…'));
		await userEvent.type(box(/recorded as the reason review ended/i), 'Enough');
		await userEvent.click(btn('End review — the run keeps waiting'));
		expect(onanswer.mock.calls[0].slice(0, 2)).toEqual(['reject', 'Enough']);
		expect(noBtn('Abandon the run')).not.toBeInTheDocument();
	});
});

describe('a manual stage', () => {
	const manual = { type: 'manual', stageId: 'work', question: 'Build it' };

	it('hands the workspace over, naming where the brief is', () => {
		const { container } = setup(manual, { runId: 'r1' });
		expect(screen.getByText('Build it')).toBeInTheDocument();
		expect(btn(/open editor/i)).toBeEnabled();
		expect(container).toHaveTextContent(
			'The brief is also in the workspace: .apothecary/manual/work.md'
		);
	});

	it('continues on Done', async () => {
		const { onanswer } = setup(manual, { runId: 'r1' });
		await userEvent.click(btn('Done — continue'));
		expect(onanswer.mock.calls[0][0]).toBe('approve');
	});
});

describe('a form with one option to pick', () => {
	const pick = question({
		form: aForm({
			id: 'env',
			options: [
				{ id: 'dev', label: 'Development', detail: 'The safe one' },
				{ id: 'prod', label: 'Production' }
			]
		})
	});

	it('gives each option a button that answers at once', async () => {
		const { onanswer } = setup(pick);
		await userEvent.click(btn('Production'));
		expect(onanswer).toHaveBeenCalledExactlyOnceWith('accept', '', undefined, { env: 'prod' });
	});

	it("shows an option's detail on hover", () => {
		setup(pick);
		expect(btn('Development')).toHaveAttribute('title', 'The safe one');
	});

	it('holds every option while one is on its way', async () => {
		const { rerender } = setup(pick);
		await userEvent.click(btn('Production'));
		await rerender({ busy: true });
		expect(btn('Development')).toBeDisabled();
		expect(btn('Production')).toHaveAttribute('aria-busy', 'true');
		expect(btn('Development')).toHaveAttribute('aria-busy', 'false');
	});
});

describe('a form with one free-text answer', () => {
	const text = (required = true) => question({ form: aForm({ id: 'why', required }) });
	const answer = () => box('Your answer to the agent…');

	it('sends the answer trimmed', async () => {
		const { onanswer } = setup(text());
		await userEvent.type(answer(), '  Because  ');
		await userEvent.click(btn('Send answer'));
		expect(onanswer).toHaveBeenCalledExactlyOnceWith('accept', '', undefined, {
			why: 'Because'
		});
	});

	it('holds a required answer until there is one', async () => {
		setup(text());
		expect(btn('Send answer')).toBeDisabled();
		await userEvent.type(answer(), '   ');
		expect(btn('Send answer')).toBeDisabled();
		await userEvent.type(answer(), 'x');
		expect(btn('Send answer')).toBeEnabled();
	});

	it('sends an optional answer blank, with nothing in it', async () => {
		const { onanswer } = setup(text(false));
		await userEvent.click(btn('Send answer'));
		expect(onanswer).toHaveBeenCalledExactlyOnceWith('accept', '', undefined, {});
	});

	it('starts from the field’s default', () => {
		setup(question({ form: aForm({ id: 'why', default: 'Because' }) }));
		expect(answer()).toHaveValue('Because');
	});

	it('sends on the keyboard', async () => {
		const { onanswer } = setup(text());
		await userEvent.type(answer(), 'Yes{Control>}{Enter}{/Control}');
		expect(onanswer).toHaveBeenCalledExactlyOnceWith('accept', '', undefined, { why: 'Yes' });
	});

	it('does not send a required blank on the keyboard', async () => {
		const { onanswer } = setup(text());
		await userEvent.type(answer(), '{Control>}{Enter}{/Control}');
		expect(onanswer).not.toHaveBeenCalled();
	});
});

describe('a form with fields', () => {
	const form = question({
		form: aForm(
			{ id: 'env', title: 'Environment', required: true, options: options('dev', 'prod') },
			{ id: 'count', type: 'integer', title: 'How many', required: true },
			{ id: 'why', title: 'Why' }
		)
	});
	const send = () => btn('Send answer');

	it('draws every field and an optional note', () => {
		setup(form);
		expect(screen.getByRole('radiogroup', { name: 'Environment' })).toBeInTheDocument();
		expect(screen.getByRole('spinbutton', { name: 'How many' })).toBeInTheDocument();
		expect(box('Why')).toBeInTheDocument();
		expect(box('Optional note')).toBeInTheDocument();
	});

	it('says what is still to answer, and sends once nothing is', async () => {
		const { onanswer } = setup(form);
		expect(send()).toBeDisabled();
		expect(send()).toHaveAttribute('title', 'Still to answer: Environment, How many');

		await userEvent.click(screen.getByRole('radio', { name: 'PROD' }));
		expect(send()).toHaveAttribute('title', 'Still to answer: How many');

		await userEvent.type(screen.getByRole('spinbutton', { name: 'How many' }), '3');
		expect(send()).toBeEnabled();
		expect(send()).toHaveAttribute('title', '');

		await userEvent.type(box('Optional note'), 'Thanks');
		await userEvent.click(send());
		expect(onanswer).toHaveBeenCalledExactlyOnceWith('accept', 'Thanks', undefined, {
			env: 'prod',
			count: 3
		});
	});

	it('names a field with no title by its id', () => {
		setup(question({ form: aForm({ id: 'a', required: true }, { id: 'b' }) }));
		expect(send()).toHaveAttribute('title', 'Still to answer: a');
	});

	it('holds the fields while busy', () => {
		setup(form, { busy: true });
		expect(screen.getByRole('spinbutton', { name: 'How many' })).toBeDisabled();
		expect(screen.getByRole('radio', { name: 'PROD' })).toBeDisabled();
	});
});

describe('a form with one field to pick any of', () => {
	const threads = (over = {}) =>
		question({
			form: aForm({ id: 'threads', type: 'array', options: options('t1', 't2', 't3'), ...over })
		});

	it('says how many it will confirm', async () => {
		setup(threads({ default: ['t1', 't2'] }));
		expect(btn('Confirm 2 selected')).toBeEnabled();
		await userEvent.click(screen.getByRole('checkbox', { name: 'T3' }));
		expect(btn('Confirm 3 selected')).toBeEnabled();
	});

	it('sends the pick', async () => {
		const { onanswer } = setup(threads({ default: ['t1', 't2'] }));
		await userEvent.click(screen.getByRole('checkbox', { name: 'T1' }));
		await userEvent.click(btn('Confirm 1 selected'));
		expect(onanswer).toHaveBeenCalledExactlyOnceWith('accept', '', undefined, {
			threads: ['t2']
		});
	});

	it('counts only when the pick is the whole form', () => {
		setup(
			question({
				form: aForm(
					{ id: 'threads', type: 'array', options: options('t1'), default: ['t1'] },
					{ id: 'why' }
				)
			})
		);
		expect(btn('Send answer')).toBeEnabled();
		expect(noBtn(/confirm/i)).not.toBeInTheDocument();
	});

	it('takes picking nothing as an answer', async () => {
		const { onanswer } = setup(threads({ required: true }));
		await userEvent.click(btn('Select none'));
		expect(onanswer).toHaveBeenCalledExactlyOnceWith('accept', '', undefined, { threads: [] });
	});
});

describe('declining a form', () => {
	const form = question({
		form: aForm({ id: 'env', title: 'Environment', required: true }, { id: 'n', type: 'number' })
	});
	const pick = question({ form: aForm({ id: 'env', options: options('dev', 'prod') }) });

	it('swaps the form for the reason box', async () => {
		setup(form);
		await userEvent.click(btn('Decline…'));
		expect(box(REASON)).toBeInTheDocument();
		expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument();
		expect(noBtn('Send answer')).not.toBeInTheDocument();
	});

	it.each([
		['a form with fields', form],
		['a pick', pick],
		['one text answer', question({ form: aForm({ id: 'why' }) })]
	])('is offered on %s', async (_, ping) => {
		setup(ping);
		expect(btn('Decline…')).toHaveAttribute(
			'title',
			'None of this fits — answer with your reason instead'
		);
	});

	it('needs a reason, then sends it with no content', async () => {
		const { onanswer } = setup(form);
		await userEvent.click(btn('Decline…'));
		expect(btn('Send decline')).toBeDisabled();

		await userEvent.type(box(REASON), 'None fit');
		await userEvent.click(btn('Send decline'));

		expect(onanswer).toHaveBeenCalledExactlyOnceWith('decline', 'None fit', undefined, undefined);
	});

	it('sends on the keyboard, reason or nothing', async () => {
		const { onanswer } = setup(form);
		await userEvent.click(btn('Decline…'));
		await userEvent.type(box(REASON), '{Control>}{Enter}{/Control}');
		expect(onanswer).not.toHaveBeenCalled();

		await userEvent.type(box(REASON), 'No{Control>}{Enter}{/Control}');
		expect(onanswer).toHaveBeenCalledExactlyOnceWith('decline', 'No', undefined, undefined);
	});

	it('comes back to the form on Cancel, with what was typed', async () => {
		setup(form);
		await userEvent.type(box('Environment'), 'staging');
		await userEvent.click(btn('Decline…'));
		await userEvent.click(btn('Cancel'));
		expect(box('Environment')).toHaveValue('staging');
	});

	it('gives a form its note back when a decline is cancelled', async () => {
		const { onanswer } = setup(form);
		await userEvent.type(box('Environment'), 'staging');
		await userEvent.type(box('Optional note'), 'for the demo');
		await userEvent.click(btn('Decline…'));
		await userEvent.type(box(REASON), ', actually no');
		await userEvent.click(btn('Cancel'));
		expect(box('Optional note')).toHaveValue('for the demo');

		await userEvent.click(btn('Send answer'));
		expect(onanswer).toHaveBeenCalledWith('accept', 'for the demo', undefined, expect.anything());
	});

	it('does not send a cancelled reason with the answer', async () => {
		const { onanswer } = setup(pick);
		await userEvent.click(btn('Decline…'));
		await userEvent.type(box(REASON), 'Changed my mind');
		await userEvent.click(btn('Cancel'));
		await userEvent.click(btn('DEV'));
		expect(onanswer).toHaveBeenCalledWith('accept', '', undefined, { env: 'dev' });
	});
});

describe('attachments', () => {
	it('keeps the picker shut until asked, and shut again on a second press', async () => {
		setup();
		expect(screen.queryByText('Attach files to your answer')).not.toBeInTheDocument();
		await userEvent.click(btn(/attach/i));
		expect(screen.getByText('Attach files to your answer')).toBeInTheDocument();
		await userEvent.click(btn(/^.?\s*attach$/i));
		expect(screen.queryByText('Attach files to your answer')).not.toBeInTheDocument();
	});

	it('counts what is attached, and keeps the picker up while there is any', async () => {
		const { container } = setup();
		await attach(container, aFile('a.txt'), aFile('b.txt'));
		await userEvent.click(btn(/attach\s*2/i));
		expect(screen.getByText('a.txt')).toBeInTheDocument();
	});

	it('uploads first and answers with the ids', async () => {
		vi.mocked(api.files.upload).mockResolvedValue([{ id: 'f1' }, { id: 'f2' }] as never);
		const files = [aFile('a.txt'), aFile('b.txt')];
		const { container, onanswer } = setup();
		await attach(container, ...files);

		await userEvent.click(btn('Approve'));

		await waitFor(() => expect(onanswer).toHaveBeenCalledOnce());
		expect(api.files.upload).toHaveBeenCalledExactlyOnceWith(files);
		expect(onanswer).toHaveBeenCalledWith('approve', '', ['f1', 'f2'], undefined);
	});

	it('carries files on a form answer too', async () => {
		vi.mocked(api.files.upload).mockResolvedValue([{ id: 'f1' }] as never);
		const { container, onanswer } = setup(
			question({ form: aForm({ id: 'env', options: options('dev') }) })
		);
		await attach(container, aFile());

		await userEvent.click(btn('DEV'));

		await waitFor(() => expect(onanswer).toHaveBeenCalledOnce());
		expect(onanswer).toHaveBeenCalledWith('accept', '', ['f1'], { env: 'dev' });
	});

	it('uploads nothing when nothing is attached', async () => {
		setup();
		await userEvent.click(btn('Approve'));
		expect(api.files.upload).not.toHaveBeenCalled();
	});

	it('holds the card while the upload runs, marking only the answer sent', async () => {
		const job = deferred<never>();
		vi.mocked(api.files.upload).mockReturnValue(job.promise);
		const { container, onanswer } = setup();
		await attach(container, aFile());

		await userEvent.click(btn('Approve'));

		expect(btn('Approve')).toBeDisabled();
		expect(btn('Approve')).toHaveAttribute('aria-busy', 'true');
		expect(btn('Request changes…')).toBeDisabled();
		expect(onanswer).not.toHaveBeenCalled();
		job.reject(new Error('stop'));
		await waitFor(() => expect(btn('Approve')).toBeEnabled());
	});

	// It used to be swallowed: the card idle, the answer silently unsent.
	it('shows a failed upload, sends nothing and keeps the draft', async () => {
		vi.mocked(api.files.upload).mockRejectedValue(new Error('too big'));
		const { container, onanswer } = setup();
		await attach(container, aFile('keep.txt'));
		await userEvent.click(btn('Request changes…'));
		await userEvent.type(box(FIELD_NOTE), 'My reason');

		await userEvent.click(btn('Send changes'));

		expect(await screen.findByText('Upload failed: too big')).toBeInTheDocument();
		expect(onanswer).not.toHaveBeenCalled();
		expect(box(FIELD_NOTE)).toHaveValue('My reason');
		expect(screen.getByText('keep.txt')).toBeInTheDocument();
		expect(btn('Send changes')).toBeEnabled();
	});

	it('clears a failed upload once a retry goes through', async () => {
		vi.mocked(api.files.upload).mockRejectedValueOnce(new Error('too big'));
		vi.mocked(api.files.upload).mockResolvedValueOnce([{ id: 'f1' }] as never);
		const { container, onanswer } = setup();
		await attach(container, aFile());
		await userEvent.click(btn('Approve'));
		await screen.findByText('Upload failed: too big');

		await userEvent.click(btn('Approve'));

		await waitFor(() => expect(onanswer).toHaveBeenCalledOnce());
		expect(screen.queryByText(/upload failed/i)).not.toBeInTheDocument();
	});
});

describe('while busy', () => {
	it('holds every way of answering a yes or no', () => {
		setup({}, { busy: true });
		expect(btn('Approve')).toBeDisabled();
		expect(btn('Request changes…')).toBeDisabled();
	});

	it('holds a phase confirmation', () => {
		setup({ type: 'phase_confirm' }, { busy: true });
		expect(btn('Run it')).toBeDisabled();
		expect(btn('Not now')).toBeDisabled();
	});

	it('holds Decline', () => {
		setup(question({ form: aForm({ id: 'why' }) }), { busy: true });
		expect(btn('Decline…')).toBeDisabled();
		expect(btn('Send answer')).toBeDisabled();
	});

	it('shows busy only on the answer the parent is sending', async () => {
		const { rerender } = setup();
		await userEvent.click(btn('Approve'));
		await rerender({ busy: true });
		expect(btn('Approve')).toHaveAttribute('aria-busy', 'true');
	});

	it('shows busy on nothing when the card did not send it', () => {
		setup({}, { busy: true });
		expect(btn('Approve')).toHaveAttribute('aria-busy', 'false');
	});
});
