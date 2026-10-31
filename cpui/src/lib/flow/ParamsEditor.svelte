<script lang="ts">
	import Checkbox from '$lib/ui/Checkbox.svelte';
	// The flow's parameters (FLOWLANG §1): the six the engine reads itself,
	// pinned at the top, and the flow's own knobs — each declared here with
	// what it holds (a type, a list of it) and its default, and read where
	// $name appears. Values come from outside the document: a workshop binds
	// them, a run may override them.
	import { t } from '$lib/i18n/index.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import Select from '$lib/ui/Select.svelte';
	import { KNOWN_PARAMS, readAsModelOrHarness, type DocParam, type FlowDoc } from '$lib/flowdoc';
	import {
		customParams,
		defaultText,
		KNOWN_TYPES,
		PARAM_TYPES,
		paramRefs,
		paramType,
		parseDefaultFor,
		renameParam,
		type ParamType
	} from './params';

	let { doc, onchange }: { doc: FlowDoc; onchange: () => void } = $props();

	const KNOWN_PLACEHOLDER: Record<string, string> = {
		harness: 'opencode',
		model: 'provider/model',
		timeout: '30m',
		max_turns: '50',
		max_cost_usd: '10',
		max_total_turns: '300'
	};
	// harness and model have no engine default: left empty, the workshop
	// binds them (or a run sets them) — the stages read them as $harness and
	// $model.
	const leftToWorkshop = (name: string) => name === 'harness' || name === 'model';

	function setKnown(name: string, text: string) {
		const v = text.trim();
		if (v === '') delete doc.parameters[name];
		else {
			const prev = doc.parameters[name];
			doc.parameters[name] = { ...prev, default: parseDefaultFor(v, KNOWN_TYPES[name]) };
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
		doc.parameters[`param_${n}`] = { type: 'string', default: '' };
		onchange();
	}

	// A new type reads the default as it: what still parses keeps, the rest
	// becomes the type's empty value. An enum is a string's.
	const EMPTY: Record<ParamType, DocParam['default']> = {
		bool: false,
		int: 0,
		float: 0,
		string: '',
		duration: '30m'
	};
	function setType(name: string, p: DocParam, type: ParamType) {
		const text = defaultText(p);
		let def = parseDefaultFor(text, type, !!p.list);
		const ok = (v: unknown) =>
			type === 'bool'
				? typeof v === 'boolean'
				: type === 'int' || type === 'float'
					? typeof v === 'number'
					: type === 'duration'
						? /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/.test(String(v))
						: true;
		if (Array.isArray(def) ? !def.every(ok) : !ok(def)) def = p.list ? [] : EMPTY[type];
		setField(name, { type, default: def, enum: type === 'string' ? p.enum : undefined });
	}
	function setList(name: string, p: DocParam, list: boolean) {
		const type = paramType(name, p);
		setField(name, {
			list: list || undefined,
			default: parseDefaultFor(defaultText(p), type, list)
		});
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
			{#if leftToWorkshop(name)}<span class="fnt">{t('flow.insp.leftToWorkshop')}</span>{:else}<span
					class="fnt">{t('flow.insp.engineDefault', { value: KNOWN_PLACEHOLDER[name] })}</span
				>{/if}</label
		>
		<input
			id="kp-{name}"
			class="mono"
			value={p == null ? '' : defaultText(p)}
			placeholder={KNOWN_PLACEHOLDER[name]}
			onchange={(e) => setKnown(name, e.currentTarget.value)}
		/>
		<!-- A value set here may say what it is for and which values a
		     workshop may bind, like any parameter. -->
		{#if p != null}
			<details class="more" open={!!(p.description || p.enum?.length)}>
				<summary class="fnt tiny">{t('flow.insp.paramMore')}</summary>
				<input
					value={p.description ?? ''}
					aria-label={t('flow.insp.description')}
					placeholder={t('flow.insp.description')}
					onchange={(e) => setField(name, { description: e.currentTarget.value || undefined })}
				/>
				<input
					class="mono"
					value={(p.enum ?? []).join(', ')}
					aria-label={t('flow.insp.paramEnum')}
					placeholder={t('flow.insp.paramEnum')}
					onchange={(e) => {
						const vals = e.currentTarget.value
							.split(',')
							.map((v) => v.trim())
							.filter(Boolean);
						setField(name, { enum: vals.length ? vals : undefined });
					}}
				/>
			</details>
		{/if}
	</div>
{/each}

<div class="lbl sub">{t('flow.insp.customParams')}</div>
{#each customParams(doc) as [name, p] (name)}
	{@const refs = paramRefs(doc, name)}
	{@const type = paramType(name, p)}
	<div class="card-in param">
		<div class="row">
			<input
				class="mono name"
				value={name}
				aria-label={t('flow.insp.paramName')}
				onchange={(e) => rename(name, e.currentTarget.value)}
			/>
			<span class="fnt tiny">{t('flow.insp.paramUsed', { count: refs.length })}</span>
			<span class="spacer"></span>
			<button class="btn-sm btn-ghost" onclick={() => remove(name)}
				>{t('flow.insp.removeParam')}</button
			>
		</div>
		<div class="row">
			<label class="fnt small" for="pt-{name}">{t('flow.insp.paramType')}</label>
			<Select
				id="pt-{name}"
				value={p.type ?? ''}
				options={[
					...(!p.type ? [{ value: '', label: '—' }] : []),
					...PARAM_TYPES.map((ty) => ({ value: ty, label: ty }))
				]}
				onchange={(v) => setType(name, p, v as ParamType)}
			/>
			<Checkbox
				size="sm"
				checked={!!p.list}
				label={t('flow.insp.paramList')}
				onchange={(c) => setList(name, p, c)}
			/>
		</div>
		{#if !p.type}<div class="help hue-gold">{t('flow.insp.paramTypeRequired')}</div>{/if}
		<div class="field">
			<label for="pd-{name}">{t('flow.insp.paramDefault')}</label>
			{#if type === 'bool' && !p.list}
				<Select
					id="pd-{name}"
					value={String(p.default)}
					options={[
						{ value: 'true', label: 'true' },
						{ value: 'false', label: 'false' }
					]}
					onchange={(v) => setField(name, { default: v === 'true' })}
				/>
			{:else}
				<input
					id="pd-{name}"
					class="mono"
					value={defaultText(p)}
					placeholder={p.list
						? t('flow.insp.paramListPlaceholder')
						: type === 'duration'
							? '30m'
							: ''}
					onchange={(e) =>
						setField(name, { default: parseDefaultFor(e.currentTarget.value, type, !!p.list) })}
				/>
			{/if}
			{#if p.list}<div class="help">{t('flow.insp.paramListHelp')}</div>{/if}
			{#if type === 'string' && !p.list && defaultText(p) === '' && readAsModelOrHarness(doc, name)}
				<div class="help">{t('flow.insp.paramUnboundHelp')}</div>
			{/if}
		</div>
		{#if type === 'string'}
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
		{/if}
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
	.more {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.more[open] > summary {
		margin-bottom: 6px;
	}
	.more > input + input {
		margin-top: 6px;
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
