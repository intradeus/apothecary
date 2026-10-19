<script lang="ts">
	import { loreLc } from '$lib/lore';
	// One configured integration (DESIGN 7.10): name, kind, masked config,
	// the persisted test result, and Test / Edit / Delete….
	import type { Integration, KindDescriptor } from '$lib/domain';
	import { api } from '$lib/api';
	import { ago, fmtAbs } from '$lib/format';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import IntegrationForm from '$lib/IntegrationForm.svelte';
	import { esc } from './html';
	import { readTests, writeTest, type TestResult } from './testResults';

	let {
		integration: i,
		descriptor,
		usedBy = [],
		onchanged
	}: {
		integration: Integration;
		descriptor?: KindDescriptor;
		/** Workshops that reference it, by name. */
		usedBy?: string[];
		onchanged: () => void;
	} = $props();

	let editing = $state(false);
	let testing = $state(false);
	let result = $state<TestResult | null>(null);
	let error = $state('');
	let now = $state(Date.now());

	$effect(() => {
		result = readTests()[i.id] ?? null;
		const t = setInterval(() => (now = Date.now()), 30_000);
		return () => clearInterval(t);
	});

	const fieldLabel = (k: string) => descriptor?.fields.find((f) => f.name === k)?.label ?? k;
	const summary = $derived([
		...Object.entries(i.config).map(([k, v]) => ({
			k: fieldLabel(k),
			v: String(v),
			secret: false
		})),
		...Object.entries(i.secrets).map(([k, v]) => ({ k: fieldLabel(k), v, secret: true }))
	]);

	async function test() {
		testing = true;
		try {
			const r = await api.integrations.test(i.id);
			result = { ok: r.ok, error: r.error, at: Date.now() };
		} catch (e) {
			result = { ok: false, error: e instanceof Error ? e.message : String(e), at: Date.now() };
		} finally {
			testing = false;
			now = Date.now();
			writeTest(i.id, result);
		}
	}

	async function remove() {
		// The engine refuses (409) while a workshop names it: say so up front.
		if (usedBy.length) {
			error = `In use by ${usedBy.join(', ')}. Pick another ${i.port} in ${usedBy.length === 1 ? `that ${loreLc('workshop')}’s` : `those ${loreLc('workshops')}’`} setup first.`;
			return;
		}
		const ok = await confirmDialog({
			title: `Delete ${i.name}?`,
			body: `The integration <b>${esc(i.name)}</b> and its stored secrets are deleted. ${i.port === 'ticketsource' ? 'Tickets already imported from it keep their copy.' : `No ${loreLc('workshop')} uses it.`}`,
			action: 'Delete integration'
		});
		if (!ok) return;
		error = '';
		try {
			await api.integrations.remove(i.id);
			writeTest(i.id, null);
			onchanged();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}
</script>

<article class="ic c" class:open={editing}>
	<div class="top">
		<div class="who">
			<div class="row nowrap" style="gap:8px">
				<span class="nm ell">{i.name}</span>
				<span class="pill t-mut">{descriptor?.label ?? i.kind}</span>
			</div>
			{#if usedBy.length}
				<div class="used ell">used by {usedBy.join(', ')}</div>
			{:else if i.port !== 'ticketsource'}
				<div class="used ell fnt">not used by any {loreLc('workshop')}</div>
			{/if}
		</div>
		<div class="res">
			{#if testing}
				<span class="pill t-az"><span class="dot pulse"></span>testing…</span>
			{:else if result}
				<span
					class="pill {result.ok ? 't-grn' : 't-ros'}"
					title="{result.error ?? 'ok'} · {fmtAbs(result.at)}"
				>
					{result.ok ? '✓ ok' : `✕ ${result.error ?? 'error'}`}
					<span class="when">· tested {ago(Math.min(result.at, now))}</span>
				</span>
			{:else}
				<span class="pill t-mut untested">not tested yet</span>
			{/if}
		</div>
	</div>

	{#if summary.length}
		<dl class="cfg">
			{#each summary as s (s.k)}
				<dt class="ell" title={s.k}>{s.k}</dt>
				<dd class="mono ell" class:secret={s.secret} title={s.secret ? 'write-only secret' : s.v}>
					{s.v || '—'}
				</dd>
			{/each}
		</dl>
	{/if}

	{#if error}<div class="err">{error}</div>{/if}

	<div class="acts">
		<button class="btn-sm" onclick={test} disabled={testing}>Test</button>
		<button class="btn-sm" class:btn-sel={editing} onclick={() => (editing = !editing)}
			>{editing ? 'Close' : 'Edit'}</button
		>
		<span class="spacer"></span>
		<button class="btn-danger btn-sm" onclick={remove}>Delete…</button>
	</div>

	{#if editing}
		<div class="edit">
			{#if descriptor}
				<IntegrationForm
					{descriptor}
					existing={i}
					onsaved={() => {
						editing = false;
						onchanged();
					}}
					oncancel={() => (editing = false)}
				/>
			{:else}
				<div class="mut small">
					Kind <code>{i.kind}</code> is not registered on this engine, so it can’t be edited here.
				</div>
			{/if}
		</div>
	{/if}
</article>

<style>
	.ic {
		border-radius: 16px;
		padding: 12px 14px 10px;
		display: flex;
		flex-direction: column;
		gap: 10px;
		min-width: 0;
	}
	.ic.open {
		grid-column: 1 / -1;
	}
	.top {
		display: flex;
		align-items: flex-start;
		gap: 10px;
	}
	.who {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.nm {
		font-weight: 600;
		font-size: 13.5px;
	}
	.used {
		font-size: 11.5px;
		color: var(--mut);
	}
	.res {
		flex: none;
		max-width: 55%;
	}
	.res .pill {
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.when {
		font-weight: 500;
		opacity: 0.8;
	}
	.untested {
		border-style: dashed;
		background: transparent;
	}
	.cfg {
		margin: 0;
		display: grid;
		grid-template-columns: minmax(0, max-content) minmax(40%, 1fr);
		gap: 3px 12px;
		font-size: 12px;
		padding: 8px 10px;
		border-radius: 10px;
		background: var(--code);
	}
	dt {
		color: var(--mut);
	}
	dd {
		margin: 0;
		font-size: 11.5px;
	}
	dd.secret {
		color: var(--mut);
	}
	.acts {
		display: flex;
		gap: 6px;
		align-items: center;
	}
	.edit {
		border-top: 1px solid var(--cLn);
		padding-top: 12px;
	}
</style>
