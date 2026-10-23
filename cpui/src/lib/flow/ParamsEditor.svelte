<script lang="ts">
	// The flow's parameters (FLOWLANG §1): the six the engine reads itself,
	// pinned at the top, and the flow's own knobs — each declared here with
	// its default, and read where $name appears. Values come from outside the
	// document: a workshop binds them, a run may override them.
	import { t } from '$lib/i18n/index.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { KNOWN_PARAMS, type DocParam, type FlowDoc } from '$lib/flowdoc';
	import { customParams, paramKind, paramRefs, parseDefault, renameParam } from './params';

	let { doc, onchange }: { doc: FlowDoc; onchange: () => void } = $props();

	const KNOWN_PLACEHOLDER: Record<string, string> = {
		harness: 'opencode',
		model: 'provider/model',
		timeout: '30m',
		max_turns: '50',
		max_cost_usd: '10',
		max_total_turns: '300'
	};
	const required = (name: string) => name === 'harness' || name === 'model';

	function setKnown(name: string, text: string) {
		const v = text.trim();
		if (v === '') delete doc.parameters[name];
		else {
			const prev = doc.parameters[name];
			doc.parameters[name] = {
				...prev,
				default: name === 'harness' || name === 'model' || name === 'timeout' ? v : parseDefault(v)
			};
		}
		onchange();
	}

	function setField(name: string, patch: Partial<DocParam>) {
		doc.parameters[name] = { ...doc.parameters[name], ...patch };
		onchange();
	}

	function add() {
		let n = 1;
		while (doc.parameters[`param_${n}`]) n++;
		doc.parameters[`param_${n}`] = { default: '' };
		onchange();
	}

	async function remove(name: string) {
		const refs = paramRefs(doc, name);
		if (refs.length) {
			const ok = await confirmDialog({
				title: t('flow.insp.removeParamTitle', { name }),
				body: t('flow.insp.removeParamBody', { count: refs.length, where: refs.join(', ') }),
				action: t('flow.insp.removeParam')
			});
			if (!ok) return;
		}
		delete doc.parameters[name];
		onchange();
	}

	function rename(from: string, to: string) {
		to = to.trim();
		if (!to || to === from || !/^[a-z][a-z0-9_]*$/.test(to) || doc.parameters[to]) return;
		renameParam(doc, from, to);
		onchange();
	}
</script>

<div class="lbl sub">{t('flow.insp.knownParams')}</div>
{#each KNOWN_PARAMS as name (name)}
	{@const p = doc.parameters[name]}
	<div class="field">
		<label for="kp-{name}"
			><span class="mono">{name}</span>
			{#if required(name)}<span class="hue-amb">{t('flow.insp.required')}</span>{:else}<span
					class="fnt">{t('flow.insp.engineDefault', { value: KNOWN_PLACEHOLDER[name] })}</span
				>{/if}</label
		>
		<input
			id="kp-{name}"
			class="mono"
			value={p == null ? '' : String(p.default)}
			placeholder={KNOWN_PLACEHOLDER[name]}
			onchange={(e) => setKnown(name, e.currentTarget.value)}
		/>
	</div>
{/each}

<div class="lbl sub">{t('flow.insp.customParams')}</div>
{#each customParams(doc) as [name, p] (name)}
	{@const refs = paramRefs(doc, name)}
	<div class="card-in param">
		<div class="row">
			<input
				class="mono name"
				value={name}
				aria-label={t('flow.insp.paramName')}
				onchange={(e) => rename(name, e.currentTarget.value)}
			/>
			<span class="pill sm fnt">{paramKind(name, p)}</span>
			<span class="fnt tiny">{t('flow.insp.paramUsed', { count: refs.length })}</span>
			<span class="spacer"></span>
			<button class="btn-sm btn-ghost" onclick={() => remove(name)}
				>{t('flow.insp.removeParam')}</button
			>
		</div>
		<div class="field">
			<label for="pd-{name}">{t('flow.insp.paramDefault')}</label>
			<input
				id="pd-{name}"
				class="mono"
				value={String(p.default)}
				onchange={(e) => setField(name, { default: parseDefault(e.currentTarget.value) })}
			/>
		</div>
		<div class="field">
			<label for="pe-{name}">{t('flow.insp.paramEnum')}</label>
			<input
				id="pe-{name}"
				class="mono"
				value={(p.enum ?? []).join(', ')}
				placeholder={t('flow.insp.paramEnumPlaceholder')}
				onchange={(e) => {
					const vals = e.currentTarget.value
						.split(',')
						.map((v) => v.trim())
						.filter(Boolean);
					setField(name, { enum: vals.length ? vals : undefined });
				}}
			/>
			<div class="help">{t('flow.insp.paramEnumHelp')}</div>
		</div>
		<div class="field">
			<label for="ps-{name}">{t('flow.insp.description')}</label>
			<input
				id="ps-{name}"
				value={p.description ?? ''}
				onchange={(e) => setField(name, { description: e.currentTarget.value || undefined })}
			/>
		</div>
	</div>
{/each}
<div class="chips">
	<button class="btn-sm" onclick={add}>{t('flow.insp.addParam')}</button>
</div>
<div class="help">{t('flow.insp.paramsReadHelp')}</div>

<style>
	.lbl.sub {
		margin-top: 8px;
	}
	.param {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 8px;
		margin-bottom: 8px;
	}
	.row {
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.name {
		max-width: 14ch;
	}
	.spacer {
		flex: 1;
	}
	.chips {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
		margin: 6px 0;
	}
</style>
