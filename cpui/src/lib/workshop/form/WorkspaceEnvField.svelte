<script lang="ts">
	// The workshop's workspace environment: name/value rows every process in
	// its workspaces gets. Plain configuration — the field says so, and sends
	// credentials to Secrets. What the engine would refuse is said beside the
	// row before a save.
	import {
		envIssues,
		MAX_ENV_VALUE_BYTES,
		MAX_ENV_VARS,
		tooManyEnvVars,
		type EnvVarIssue,
		type EnvVarRow
	} from '$lib/domain';
	import { I } from '$lib/design/icons';
	import { t, th } from '$lib/i18n/index.svelte';

	let { rows = $bindable() }: { rows: EnvVarRow[] } = $props();

	const issues = $derived(envIssues(rows));
	const tooMany = $derived(tooManyEnvVars(rows));

	function add() {
		rows = [...rows, { name: '', value: '' }];
	}
	function remove(i: number) {
		rows = rows.filter((_, j) => j !== i);
	}
	function issueText(issue: EnvVarIssue): string {
		switch (issue.kind) {
			case 'reservedPrefix':
				return t('form.project.envIssue.reservedPrefix', { prefix: issue.prefix });
			case 'tooLong':
				return t('form.project.envIssue.tooLong', { max: MAX_ENV_VALUE_BYTES });
			default:
				return t(`form.project.envIssue.${issue.kind}`);
		}
	}
</script>

<div class="row">
	<h3>{t('form.project.env')}</h3>
	<span class="count">{rows.length}</span>
	<span class="spacer"></span>
	<button type="button" class="btn-sm" onclick={add}>{I.add} {t('form.project.envAdd')}</button>
</div>
<div class="help">{@html th('form.project.envHelp')}</div>
<div class="help warn">{t('form.project.envNotSecret')}</div>
{#if rows.length}
	<table class="ev form-dense">
		<tbody>
			{#each rows as r, i (i)}
				{@const label = r.name.trim() || t('form.project.envUnnamed')}
				<tr>
					<td
						><input
							class="mono"
							bind:value={r.name}
							placeholder="JEST_MAX_WORKERS"
							aria-label={t('form.project.envName')}
							aria-invalid={issues[i] !== undefined}
							spellcheck="false"
							autocomplete="off"
						/></td
					>
					<td
						><input
							class="mono"
							bind:value={r.value}
							placeholder={'${APO_CPUS}'}
							aria-label={t('form.project.envValue', { name: label })}
							spellcheck="false"
							autocomplete="off"
						/></td
					>
					<td
						><button
							type="button"
							class="btn-icon"
							onclick={() => remove(i)}
							aria-label={t('form.project.envRemove', { name: label })}>{I.close}</button
						></td
					>
				</tr>
				{#if issues[i]}
					<tr class="issue">
						<td colspan="3"><div class="error small">{issueText(issues[i])}</div></td>
					</tr>
				{/if}
			{/each}
		</tbody>
	</table>
{/if}
{#if tooMany}
	<div class="error small">{t('form.project.envTooMany', { max: MAX_ENV_VARS })}</div>
{/if}

<style>
	.ev td {
		padding: 3px 3px;
		border-top: 0;
	}
	.ev td:first-child {
		width: 40%;
	}
	.ev td:last-child {
		width: 28px;
	}
	.ev tr.issue td {
		padding-top: 0;
	}
	.help.warn {
		color: var(--warning-text);
	}
</style>
