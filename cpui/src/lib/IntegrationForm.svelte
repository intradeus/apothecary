<script lang="ts">
	// Generic integration form rendered from the kind's Descriptor fields
	// (DESIGN 4.14, 7.10): adding a backend kind needs no frontend change.
	// Secrets are write-only: on edit a set secret shows "•••• set" and a
	// Replace button; leaving it alone keeps the stored value.
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { Integration, KindDescriptor } from '$lib/domain';

	let {
		descriptor,
		existing = null,
		onsaved,
		oncancel
	}: {
		descriptor: KindDescriptor;
		existing?: Integration | null;
		onsaved: (saved: Integration) => void;
		oncancel?: () => void;
	} = $props();

	// Read once: the form mounts fresh per integration and kind.
	const init = untrack(() => ({ d: descriptor, e: existing }));
	const fields = init.d.fields ?? [];
	let name = $state(init.e?.name ?? init.d.label);
	let values = $state<Record<string, string>>(
		Object.fromEntries(
			fields
				.filter((f) => !f.secret)
				.map((f) => [f.name, String(init.e?.config?.[f.name] ?? f.default ?? '')])
		)
	);
	let secretValues = $state<Record<string, string>>(
		Object.fromEntries(fields.filter((f) => f.secret).map((f) => [f.name, '']))
	);
	/** Secrets the user chose to replace (only meaningful when one is set). */
	let replacing = $state<Record<string, boolean>>({});
	let busy = $state(false);
	let error = $state('');

	const isMultiline = (n: string) => n.endsWith('_json');
	const storedMask = (n: string) => init.e?.secrets?.[n] ?? '';
	const showSecretInput = (n: string) => !storedMask(n) || replacing[n];

	// Descriptor help is plain text (it comes from the Go kind), but the URLs in
	// it are the whole point — "go here to make a token" is useless if you have
	// to copy it by hand. Split into text/link parts and render anchors.
	type Part = { text: string; href?: string };
	function linkify(s: string): Part[] {
		const parts: Part[] = [];
		const re = /https?:\/\/[^\s<>()]+[^\s<>().,;:]/g;
		let last = 0;
		for (const m of s.matchAll(re)) {
			const i = m.index ?? 0;
			if (i > last) parts.push({ text: s.slice(last, i) });
			parts.push({ text: m[0], href: m[0] });
			last = i + m[0].length;
		}
		if (last < s.length) parts.push({ text: s.slice(last) });
		return parts;
	}

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			const config: Record<string, unknown> = {};
			for (const f of fields.filter((f) => !f.secret)) {
				const v = values[f.name]?.trim() ?? '';
				if (v !== '') config[f.name] = v;
			}
			// Only send secret keys the user actually typed — absent keys keep
			// their stored value (§3 merge semantics).
			const secrets: Record<string, string> = {};
			for (const [k, v] of Object.entries(secretValues)) {
				if (v.trim() !== '') secrets[k] = v;
			}
			const saved = existing
				? await api.integrations.update(existing.id, { name: name.trim(), config, secrets })
				: await api.integrations.create({
						port: descriptor.port,
						kind: descriptor.kind,
						name: name.trim(),
						config,
						secrets
					});
			onsaved(saved);
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}

	const uid = Math.random().toString(36).slice(2, 7);
</script>

<form class="if" onsubmit={submit}>
	{#if descriptor.help}
		<details class="howto" open={!existing}>
			<summary><span class="caretglyph">▸</span> How to set this up</summary>
			<div class="helptext">
				{#each linkify(descriptor.help) as p, i (i)}
					{#if p.href}
						<a class="link" href={p.href} target="_blank" rel="noreferrer">{p.text}</a>
					{:else}{p.text}{/if}
				{/each}
			</div>
		</details>
	{/if}

	<div class="grid2">
		<div class="field">
			<label for="if-{uid}-name">Name</label>
			<input id="if-{uid}-name" bind:value={name} required />
			<div class="help">How setup forms and pickers refer to it.</div>
		</div>
		{#each fields as f (f.name)}
			<div class="field" class:wide={f.secret && isMultiline(f.name)}>
				<label for="if-{uid}-{f.name}"
					>{f.label}{#if f.required && !(f.secret && storedMask(f.name))}<span class="req">
							*</span
						>{/if}</label
				>
				{#if f.secret && !showSecretInput(f.name)}
					<div class="secret-set">
						<span class="mask mono">•••• set</span>
						<span class="mono fnt ell">{storedMask(f.name)}</span>
						<span class="spacer"></span>
						<button
							type="button"
							class="btn-ghost btn-sm"
							onclick={() => (replacing = { ...replacing, [f.name]: true })}>Replace</button
						>
					</div>
				{:else if f.secret && isMultiline(f.name)}
					<textarea
						id="if-{uid}-{f.name}"
						bind:value={secretValues[f.name]}
						placeholder={f.placeholder ?? ''}
						rows="5"
						class="mono"
						required={f.required && !storedMask(f.name)}
						spellcheck="false"></textarea>
				{:else if f.secret}
					<input
						id="if-{uid}-{f.name}"
						type="password"
						bind:value={secretValues[f.name]}
						placeholder={f.placeholder ?? ''}
						required={f.required && !storedMask(f.name)}
						autocomplete="off"
					/>
				{:else}
					<input
						id="if-{uid}-{f.name}"
						bind:value={values[f.name]}
						placeholder={f.placeholder ?? f.default ?? ''}
						required={f.required && !f.default}
					/>
				{/if}
				{#if f.secret && storedMask(f.name) && replacing[f.name]}
					<div class="help">
						Leave empty to keep the current value ·
						<button
							type="button"
							class="linklike"
							onclick={() => {
								replacing = { ...replacing, [f.name]: false };
								secretValues[f.name] = '';
							}}>keep current</button
						>
					</div>
				{:else if f.help}
					<div class="help">
						{#each linkify(f.help) as p, i (i)}
							{#if p.href}
								<a class="link" href={p.href} target="_blank" rel="noreferrer">{p.text}</a>
							{:else}{p.text}{/if}
						{/each}
					</div>
				{:else if f.secret && !storedMask(f.name)}
					<div class="help">Write-only: stored encrypted, never shown again.</div>
				{/if}
			</div>
		{/each}
	</div>

	{#if error}<div class="err">{error}</div>{/if}
	<div class="foot">
		{#if oncancel}<button type="button" class="btn-ghost" onclick={oncancel}>Cancel</button>{/if}
		<button class="btn-primary" type="submit" disabled={busy}>
			{busy ? 'Saving…' : existing ? 'Save changes' : `Add ${descriptor.label}`}
		</button>
	</div>
</form>

<style>
	.if {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.howto {
		background: var(--p2);
		border: 1px solid var(--ln);
		border-radius: 13px;
	}
	.howto > summary {
		padding: 7px 12px;
		font-size: 12px;
		font-weight: 600;
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.caretglyph {
		font-size: 10px;
		color: var(--mut);
	}
	.helptext {
		white-space: pre-wrap;
		font-size: 12.5px;
		line-height: 1.55;
		padding: 0 12px 10px 28px;
		color: var(--mut);
		overflow-wrap: anywhere;
	}
	.wide {
		grid-column: 1 / -1;
	}
	.req {
		color: var(--fnt);
	}
	.secret-set {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 4px 4px 4px 10px;
		border-radius: 10px;
		background: var(--p1);
		border: 1px dashed var(--ln2);
		min-height: 34px;
		min-width: 0;
	}
	.mask {
		color: var(--grn);
		font-size: 11.5px;
		flex: none;
	}
	.help :global(.link),
	.helptext :global(.link) {
		color: var(--az);
	}
	.foot {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
	}
</style>
