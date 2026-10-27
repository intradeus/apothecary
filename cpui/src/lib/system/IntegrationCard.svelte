<script lang="ts">
	// One configured integration (DESIGN 7.10): name, kind, masked config,
	// the persisted test result, and Test / Edit / Delete….
	import type { Integration, KindDescriptor } from '$lib/domain';
	import { api } from '$lib/api';
	import { ago, fmtAbs } from '$lib/format';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import ConfigCard from '$lib/ui/ConfigCard.svelte';
	import CheckChip from '$lib/ui/CheckChip.svelte';
	import IntegrationForm from '$lib/IntegrationForm.svelte';
	import { t, th } from '$lib/i18n/index.svelte';
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
	let removing = $state(false);
	let now = $state(Date.now());

	$effect(() => {
		result = readTests()[i.id] ?? null;
		const timer = setInterval(() => (now = Date.now()), 30_000);
		return () => clearInterval(timer);
	});

	const fieldLabel = (k: string) => descriptor?.fields.find((f) => f.name === k)?.label ?? k;
	const summary = $derived([
		...Object.entries(i.config).map(([k, v]) => ({ k: fieldLabel(k), v: String(v) })),
		...Object.entries(i.secrets).map(([k, v]) => ({ k: fieldLabel(k), v, secret: true }))
	]);

	async function test() {
		testing = true;
		try {
			const r = await api.integrations.test(i.id);
			result = { ok: r.ok, error: r.error, warnings: r.warnings, at: Date.now() };
		} catch (e) {
			result = { ok: false, error: e instanceof Error ? e.message : String(e), at: Date.now() };
		} finally {
			testing = false;
			now = Date.now();
			writeTest(i.id, result);
		}
		// The verdict lands on the card; the toast confirms it for a card
		// scrolled out of view while the test ran.
		if (result?.ok && result.warnings?.length)
			toast(t('system.int.passedPartly', { name: i.name }));
		else if (result?.ok) toast(t('system.int.passed', { name: i.name }));
		else toast(t('system.int.failed', { name: i.name }), { tone: 'bad' });
	}

	async function remove() {
		// The engine refuses (409) while a workshop names it: say so up front.
		if (usedBy.length) {
			error = t('system.int.inUse', {
				workshops: usedBy.join(', '),
				port: i.port,
				those: usedBy.length === 1 ? t('system.int.thatWorkshop') : t('system.int.thoseWorkshops')
			});
			return;
		}
		const ok = await confirmDialog({
			title: t('system.int.deleteTitle', { name: i.name }),
			body: th(
				i.port === 'ticketsource' ? 'system.int.deleteBodyTickets' : 'system.int.deleteBodyUnused',
				{ name: i.name }
			),
			action: t('system.int.deleteAction')
		});
		if (!ok) return;
		error = '';
		removing = true;
		try {
			await api.integrations.remove(i.id);
			writeTest(i.id, null);
			toast(t('system.int.deleted', { name: i.name }));
			onchanged();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			removing = false;
		}
	}
</script>

<ConfigCard
	title={i.name}
	kind={descriptor?.label ?? i.kind}
	{summary}
	{error}
	open={editing}
	sub={usedBy.length
		? t('system.int.usedBy', { workshops: usedBy.join(', ') })
		: i.port !== 'ticketsource'
			? t('system.int.unused')
			: undefined}
>
	{#snippet status()}
		{#if testing}
			<CheckChip tone="live" label={t('system.int.testing')} />
		{:else if result}
			{@const warnings = result.warnings ?? []}
			<CheckChip
				tone={!result.ok ? 'bad' : warnings.length ? 'warn' : 'ok'}
				label={!result.ok
					? (result.error ?? t('system.int.errorLabel'))
					: warnings.length
						? t('system.int.okUnverified')
						: t('system.int.ok')}
				when={t('system.int.tested', { ago: ago(Math.min(result.at, now)) })}
				title="{[result.error ?? 'ok', ...warnings].join('\n')} · {fmtAbs(result.at)}"
			/>
		{:else}
			<CheckChip tone="none" label={t('system.int.notTested')} />
		{/if}
	{/snippet}
	{#snippet actions()}
		<button class="btn-sm" onclick={test} disabled={testing} aria-busy={testing}
			>{t('system.int.test')}</button
		>
		<button
			class="btn-sm"
			class:btn-sel={editing}
			aria-pressed={editing}
			onclick={() => (editing = !editing)}
			>{editing ? t('system.int.close') : t('system.int.edit')}</button
		>
		<span class="spacer"></span>
		<button class="btn-danger btn-sm" onclick={remove} aria-busy={removing}
			>{t('system.int.delete')}</button
		>
	{/snippet}
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
		<div class="mut small">{@html th('system.int.unregistered', { kind: i.kind })}</div>
	{/if}
</ConfigCard>
