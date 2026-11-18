<script lang="ts">
	// Go to path (DESIGN §7.1): any file or folder the workspace's user can
	// read, by an absolute path or one under ~. The folder typed so far lists
	// below as you type; ↑ ↓ pick an entry, Tab completes it (a folder then
	// lists in turn), Enter opens it: a file in a tab, a folder as the tree's
	// root. A file is read first, so one that can't be read says why here and
	// opens nothing. A click on a listed folder goes into it; on a file, opens
	// it.
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import { ApiError } from '$lib/api/client';
	import type { WorkspaceEntry } from '$lib/domain';
	import { errorMessage } from '$lib/core/errors';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import Modal from '$lib/ui/Modal.svelte';
	import { indicator } from '$lib/ui/motion';

	let {
		runId,
		open,
		onclose,
		onfile,
		ondir
	}: {
		runId: string;
		open: boolean;
		onclose: () => void;
		/** A file that was read: its expanded, absolute path. */
		onfile: (abs: string) => void;
		/** A folder, as typed (it may start with ~). */
		ondir: (path: string) => void;
	} = $props();

	const uid = $props.id();
	/** How long typing pauses before the folder is listed. */
	const DEBOUNCE = 150;

	let value = $state('~/');
	/** The highlighted entry; -1 = the typed path itself. */
	let hi = $state(-1);
	let listing = $state<{ dir: string; entries: WorkspaceEntry[]; error: string } | null>(null);
	let error = $state('');
	let busy = $state(false);
	/** The latest listing asked for: an older answer is dropped. */
	let asked = 0;

	const valid = $derived(value.startsWith('/') || value.startsWith('~'));
	const cut = $derived(value.lastIndexOf('/') + 1);
	/** The folder part, up to and including the last '/'. */
	const dir = $derived(valid ? value.slice(0, cut) : '');
	/** What is typed of the name inside it. */
	const rest = $derived(value.slice(cut));
	const here = $derived(listing?.dir === dir ? listing : null);
	const matches = $derived(
		(here?.entries ?? []).filter((e) => e.name.toLowerCase().startsWith(rest.toLowerCase()))
	);

	/** A folder as the routes take it: no trailing slash, but for the root. */
	const bare = (p: string) => (p.length > 1 && p.endsWith('/') ? p.slice(0, -1) : p);
	const explain = (e: unknown) =>
		e instanceof ApiError && e.status === 501 ? t('run.files.goto.predates') : errorMessage(e);

	// Each opening starts afresh, at home.
	$effect(() => {
		if (!open) return;
		untrack(() => {
			value = '~/';
			hi = -1;
			error = '';
			listing = null;
		});
	});

	$effect(() => {
		if (!open || !dir) return;
		const d = dir;
		const timer = setTimeout(async () => {
			const seq = ++asked;
			try {
				const entries = await api.workspace.pathTree(runId, bare(d));
				if (seq === asked) listing = { dir: d, entries, error: '' };
			} catch (e) {
				if (seq === asked) listing = { dir: d, entries: [], error: explain(e) };
			}
		}, DEBOUNCE);
		return () => clearTimeout(timer);
	});

	function complete(e: WorkspaceEntry) {
		value = dir + e.name + (e.dir ? '/' : '');
		hi = -1;
		error = '';
	}

	/** Open the highlighted entry, else the one named exactly, else the
	 *  typed path: a folder is one the listing says is, or one typed with a
	 *  trailing '/'; anything else is read as a file, and a path refused as
	 *  a file (400) that lists is a folder after all. */
	async function go(entry = hi >= 0 ? matches[hi] : here?.entries.find((e) => e.name === rest)) {
		if (busy || !valid) return;
		const path = entry ? dir + entry.name : value;
		let folder = entry ? entry.dir : value === '~' || value.endsWith('/');
		if (!folder) {
			busy = true;
			error = '';
			try {
				onfile((await api.workspace.pathFile(runId, path)).path);
			} catch (e) {
				folder =
					!entry &&
					e instanceof ApiError &&
					e.status === 400 &&
					(await api.workspace.pathTree(runId, path).then(
						() => true,
						() => false
					));
				if (!folder) {
					error = explain(e);
					return;
				}
			} finally {
				busy = false;
			}
		}
		if (folder) ondir(bare(path));
		onclose();
	}

	function key(e: KeyboardEvent) {
		const n = matches.length;
		if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
			e.preventDefault();
			if (n) hi = e.key === 'ArrowDown' ? Math.min(hi + 1, n - 1) : Math.max(hi - 1, -1);
		} else if (e.key === 'Tab' && !e.shiftKey && n) {
			e.preventDefault();
			complete(matches[Math.max(hi, 0)]);
		} else if (e.key === 'Enter') {
			e.preventDefault();
			void go();
		}
	}
</script>

<Modal {open} {onclose} title={t('run.files.goto.title')} icon={I.search} width={520}>
	<div class="f">
		<div class="field">
			<label for="{uid}-in">{t('run.files.goto.label')}</label>
			<input
				id="{uid}-in"
				class="mono"
				role="combobox"
				aria-autocomplete="list"
				aria-expanded={matches.length > 0}
				aria-controls={here && !here.error ? `${uid}-list` : undefined}
				aria-activedescendant={hi >= 0 ? `${uid}-${hi}` : undefined}
				autocomplete="off"
				spellcheck="false"
				bind:value
				oninput={() => {
					hi = -1;
					error = '';
				}}
				onkeydown={key}
			/>
			{#if valid || !value}
				<span class="help">{t('run.files.goto.help')}</span>
			{:else}
				<span class="error">{t('run.files.goto.absolute')}</span>
			{/if}
		</div>
		{#if here?.error}
			<div class="faint">{here.error}</div>
		{:else if here}
			<div
				id="{uid}-list"
				class="list"
				role="listbox"
				aria-label={t('run.files.goto.entries')}
				use:indicator
			>
				{#each matches as m, i (m.path)}
					<button
						id="{uid}-{i}"
						type="button"
						tabindex="-1"
						class="menu-item mono"
						class:on={i === hi}
						role="option"
						aria-selected={i === hi}
						onclick={() => (m.dir ? complete(m) : go(m))}
					>
						<span class="g" aria-hidden="true">{m.dir ? I.folder : I.file}</span>
						<span class="truncate grow">{m.name}{m.dir ? '/' : ''}</span>
					</button>
				{:else}
					<div class="empty-line">{t('run.files.goto.none')}</div>
				{/each}
			</div>
		{/if}
		{#if error}<div class="error">{error}</div>{/if}
	</div>
	{#snippet footer()}
		<button class="btn-ghost" onclick={onclose}>{t('common.cancel')}</button>
		<button class="btn-primary" disabled={busy || !valid} aria-busy={busy} onclick={() => go()}
			>{t('run.files.goto.open')}</button
		>
	{/snippet}
</Modal>

<style>
	.f {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.list {
		max-height: 260px;
		overflow: auto;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.list > :global(.ind) {
		border-radius: var(--r-row);
	}
	.menu-item {
		flex: none;
		font-size: var(--fs-body);
	}
	.faint {
		overflow-wrap: anywhere;
	}
	.error {
		overflow-wrap: anywhere;
	}
</style>
