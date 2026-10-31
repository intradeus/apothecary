import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { ReviewThread } from '$lib/domain';
import { bindable } from '$test/fixtures/app.svelte';
import {
	aContinuation,
	aNote,
	aReply,
	aRepliesDoc,
	aReviewDoc,
	aThread
} from '$test/fixtures/curation';
import { initialState, itemsOf, type CurItem } from './curation';
import CurationItem from './CurationItem.svelte';

const note = (over: Parameters<typeof aNote>[0] = {}) =>
	itemsOf(aReviewDoc({ notes: [aNote({ body: 'Charges twice.', ...over })] }))[0];
const continuation = () =>
	itemsOf(aReviewDoc({ replies: [aContinuation({ thread: 'th-1', body: 'Still open.' })] }))[0];
const reply = () =>
	itemsOf(aRepliesDoc([aReply({ thread: 'th-1', reply: 'Fixed it.', resolve: true })]))[0];

function setup(
	item: CurItem,
	over: { thread?: ReviewThread; isReply?: boolean; disabled?: boolean } = {}
) {
	const doc = aReviewDoc();
	const state = initialState(doc, [item]).items[item.key];
	const props = bindable({ item, state, onchange: vi.fn(), ...over });
	render(CurationItem, props);
	return props;
}

const textBox = () => screen.getByRole('textbox', { name: 'The text as it will be posted' });
const restore = () => screen.queryByRole('button', { name: 'Restore as written' });
const resolveBox = () => screen.queryByRole('checkbox', { name: 'Resolve the thread' });
const keepBox = (name: string) => screen.getByRole('checkbox', { name });
const pill = (name: string) => screen.getByText(name, { exact: false, selector: '.pill' });

describe('CurationItem', () => {
	it('shows a note with its kind, place and text, and nothing to resolve', () => {
		setup(note());
		expect(screen.getByText('blocking')).toHaveClass('tint-danger');
		expect(screen.getByText('src/pay.go:12')).toBeInTheDocument();
		expect(textBox()).toHaveValue('Charges twice.');
		expect(resolveBox()).not.toBeInTheDocument();
		expect(restore()).not.toBeInTheDocument();
	});

	it('asks whether to post a note', () => {
		setup(note());
		expect(keepBox('Post it')).toBeChecked();
	});

	it('asks to send a reply rather than post it', () => {
		setup(reply(), { isReply: true });
		expect(keepBox('Send it')).toBeChecked();
	});

	it('words the thread as continued on a review', () => {
		setup(continuation(), { thread: aThread() });
		expect(pill('continues a thread')).toHaveTextContent('continues a thread th-1');
	});

	it('words the thread as answered on a round', () => {
		setup(reply(), { isReply: true, thread: aThread() });
		expect(pill('answers')).toHaveTextContent('answers th-1');
	});

	it('shows the thread the item is in', () => {
		setup(reply(), { isReply: true, thread: aThread() });
		expect(screen.getByText('Why is this retried?')).toBeInTheDocument();
	});

	it('says so when the thread is not among the files shown', () => {
		setup(reply(), { isReply: true });
		expect(screen.getByText('thread th-1 is not among the files shown')).toBeInTheDocument();
	});

	it('marks a text reworded and gives the way back', async () => {
		const props = setup(note());
		await userEvent.type(textBox(), ' Always.');

		expect(screen.getByText('reworded')).toBeInTheDocument();
		expect(props.state.body).toBe('Charges twice. Always.');
		expect(props.onchange).toHaveBeenCalled();

		props.onchange.mockClear();
		await userEvent.click(restore()!);

		expect(textBox()).toHaveValue('Charges twice.');
		expect(props.state.body).toBe('Charges twice.');
		expect(props.onchange).toHaveBeenCalledOnce();
		expect(screen.queryByText('reworded')).not.toBeInTheDocument();
	});

	it('does not call spaces around the text a rewording', async () => {
		setup(note());
		await userEvent.type(textBox(), '   ');
		expect(screen.queryByText('reworded')).not.toBeInTheDocument();
	});

	it('warns about a kept text that is blank', async () => {
		setup(note());
		await userEvent.clear(textBox());
		expect(screen.getByText('Empty — write something, or drop it.')).toBeInTheDocument();
	});

	it('does not warn about a blank text that is dropped', async () => {
		const props = setup(note());
		await userEvent.clear(textBox());
		await userEvent.click(keepBox('Post it'));

		expect(props.state.keep).toBe(false);
		expect(screen.queryByText('Empty — write something, or drop it.')).not.toBeInTheDocument();
	});

	it('tells the page an item was dropped', async () => {
		const props = setup(note());
		await userEvent.click(keepBox('Post it'));

		expect(props.state.keep).toBe(false);
		expect(props.onchange).toHaveBeenCalledOnce();
	});

	it("changes a reply's resolve flag", async () => {
		const props = setup(reply(), { isReply: true, thread: aThread() });
		expect(resolveBox()).toBeChecked();

		await userEvent.click(resolveBox()!);

		expect(props.state.resolve).toBe(false);
		expect(props.onchange).toHaveBeenCalledOnce();
	});

	it('has no resolve flag to set on a dropped item', async () => {
		setup(reply(), { isReply: true, thread: aThread() });
		await userEvent.click(keepBox('Send it'));
		expect(resolveBox()).toBeDisabled();
	});

	it('holds every control while disabled', async () => {
		const props = setup(reply(), { isReply: true, thread: aThread(), disabled: false });
		await userEvent.type(textBox(), ' More.');
		props.disabled = true;

		await vi.waitFor(() => expect(textBox()).toBeDisabled());
		expect(keepBox('Send it')).toBeDisabled();
		expect(resolveBox()).toBeDisabled();
		expect(restore()).toBeDisabled();
	});

	it('gives the box room for what it holds', () => {
		setup(note({ body: 'one\ntwo\nthree\nfour' }));
		expect(textBox()).toHaveAttribute('rows', '4');
	});

	it('folds away only the reasons the agent gave', () => {
		setup(note({ rationale: 'It retries.', example: 'pay(1); pay(1)' }));

		const fold = screen.getByText('Why the agent says so — never posted').closest('details')!;
		expect(fold).not.toHaveAttribute('open');
		expect(screen.getByText('Rationale')).toBeInTheDocument();
		expect(screen.getByText('It retries.')).toBeInTheDocument();
		expect(screen.getByText('Example')).toBeInTheDocument();
		expect(screen.queryByText('Evidence')).not.toBeInTheDocument();
	});

	it('offers no fold when the agent gave no reasons', () => {
		setup(note());
		expect(screen.queryByText('Why the agent says so — never posted')).not.toBeInTheDocument();
	});

	it('opens the fold for evidence alone', () => {
		setup(note({ evidence: 'See the log.' }));
		expect(screen.getByText('Evidence')).toBeInTheDocument();
		expect(screen.getByText('See the log.')).toBeInTheDocument();
	});
});
