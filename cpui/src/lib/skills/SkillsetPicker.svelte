<script lang="ts">
	// Which skillsets a run takes along (TODO 38): any of one's own, each at
	// the latest version unless an older one is picked. Versions load when a
	// set is ticked.
	import { errorMessage } from '$lib/core/errors';
	import { api } from '$lib/api';
	import type { Skillset, SkillsetRef, SkillsetVersion } from '$lib/domain';
	import { ago } from '$lib/core/format';
	import { t } from '$lib/i18n/index.svelte';
	import { to } from '$lib/core/urls';
	import CheckboxGroup from '$lib/ui/CheckboxGroup.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';

	let {
		value = $bindable([]),
		idPrefix = 'sp'
	}: {
		/** The picked skillsets; seq undefined = the latest. */
		value: SkillsetRef[];
		idPrefix?: string;
	} = $props();

	let sets = $state<Skillset[] | null>(null);
	let error = $state('');
	let versions = $state<Record<string, SkillsetVersion[]>>({});

	$effect(() => {
		let live = true;
		api.skillsets.list().then(
			(rows) => {
				if (!live) return;
				sets = rows;
				error = '';
				// A pick of a set that is gone drops.
				const ids = new Set(rows.map((s) => s.id));
				const kept = value.filter((r) => ids.has(r.id));
				if (kept.length !== value.length) value = kept;
			},
			(e: unknown) => {
				if (!live) return;
				sets = [];
				error = errorMessage(e);
			}
		);
		return () => {
			live = false;
		};
	});

	const asked = new Set<string>();
	$effect(() => {
		for (const r of value) {
			if (asked.has(r.id)) continue;
			asked.add(r.id);
			api.skillsets.versions(r.id).then(
				(vs) => (versions = { ...versions, [r.id]: vs }),
				() => (versions = { ...versions, [r.id]: [] })
			);
		}
	});

	const options = $derived(
		(sets ?? []).map((s) => ({
			value: s.id,
			label: s.name,
			detail: s.description || t('skills.version', { seq: String(s.headSeq) })
		}))
	);
	const picked = $derived(value.map((r) => r.id));
	const byId = $derived(new Map((sets ?? []).map((s) => [s.id, s])));

	function pick(ids: string[]) {
		value = ids.map((id) => value.find((r) => r.id === id) ?? { id });
	}
	function setSeq(id: string, seq: number) {
		value = value.map((r) => (r.id === id ? { id, ...(seq ? { seq } : {}) } : r));
	}
	const versionOptions = (id: string): SelectOption[] => [
		{ value: '0', label: t('run.start.latest'), detail: `v${byId.get(id)?.headSeq ?? ''}` },
		...(versions[id] ?? []).map((v) => ({
			value: String(v.seq),
			label: `v${v.seq}`,
			detail: `@${v.hash.slice(0, 8)} · ${ago(v.createdAt)}`
		}))
	];
</script>

{#if sets === null}
	<span class="help">{t('ui.loading')}</span>
{:else if error}
	<span class="help hue-warning">{error}</span>
{:else if !sets.length}
	<span class="help"
		>{t('run.start.skillsetsNone')} <a href={to.skills()}>{t('skills.crumb')}</a></span
	>
{:else}
	<CheckboxGroup {options} value={picked} onchange={pick} label={t('run.start.skillsets')} />
	{#if value.length}
		<div class="vers">
			{#each value as r (r.id)}
				<div class="vr">
					<label for="{idPrefix}-v-{r.id}" class="truncate">{byId.get(r.id)?.name ?? r.id}</label>
					<Select
						id="{idPrefix}-v-{r.id}"
						size="sm"
						inline
						options={versionOptions(r.id)}
						value={String(r.seq ?? 0)}
						onchange={(v) => setSeq(r.id, Number(v))}
						title={t('run.start.skillsetVersion', { name: byId.get(r.id)?.name ?? r.id })}
					/>
				</div>
			{/each}
		</div>
	{/if}
	<span class="help">{t('run.start.skillsetsHelp')}</span>
{/if}

<style>
	.vers {
		display: flex;
		flex-direction: column;
		gap: 4px;
		margin-top: 6px;
	}
	.vr {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		font-size: var(--fs-label);
	}
	.vr label {
		color: var(--text-muted);
		min-width: 0;
	}
</style>
