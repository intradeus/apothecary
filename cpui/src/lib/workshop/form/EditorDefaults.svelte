<script lang="ts" module>
	/** The editor defaults as the form edits them: a number input's value is
	 *  '' while empty. */
	export interface EditorFields {
		theme: string;
		fontSize: number | '';
		tabSize: number | '';
		formatOnSave: boolean;
		wordWrap: boolean;
		trustWorkspace: boolean;
	}
</script>

<script lang="ts">
	// code-server UI defaults (applied as VS Code user settings at editor open,
	// no rebuild). Theme names are the built-in VS Code themes; a themed
	// extension can be added to the extensions list and named here.
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import FormSub from './FormSub.svelte';

	let { editor = $bindable() }: { editor: EditorFields } = $props();

	const editorThemes = [
		'Default Dark Modern',
		'Default Light Modern',
		'Default Dark+',
		'Default Light+',
		'Default High Contrast',
		'Monokai',
		'Monokai Dimmed',
		'Solarized Dark',
		'Solarized Light',
		'Quiet Light',
		'Abyss',
		'Kimbie Dark',
		'Red',
		'Tomorrow Night Blue'
	];
	const themeOptions = $derived<SelectOption[]>([
		{ value: '', label: t('form.project.themeDefault') },
		...editorThemes.map((theme) => ({ value: theme, label: theme }))
	]);
</script>

<FormSub>
	<h3>{t('form.project.editorDefaults')}</h3>
	<div class="help">{t('form.project.editorHelp')}</div>
	<div class="ed">
		<div class="field">
			<label for="p-theme">{t('form.project.colorTheme')}</label>
			<Select id="p-theme" options={themeOptions} bind:value={editor.theme} />
		</div>
		<div class="field">
			<label for="p-font">{t('form.project.fontSize')}</label>
			<input
				id="p-font"
				type="number"
				min="0"
				max="40"
				bind:value={editor.fontSize}
				placeholder={t('form.project.default')}
			/>
		</div>
		<div class="field">
			<label for="p-tab">{t('form.project.tabSize')}</label>
			<input
				id="p-tab"
				type="number"
				min="0"
				max="16"
				bind:value={editor.tabSize}
				placeholder={t('form.project.default')}
			/>
		</div>
	</div>
	<div class="row toggles">
		<Toggle bind:checked={editor.formatOnSave} label={t('form.project.formatOnSave')} />
		<Toggle bind:checked={editor.wordWrap} label={t('form.project.wordWrap')} />
		<Toggle
			bind:checked={editor.trustWorkspace}
			label={t('form.project.trustWorkspace')}
			title={t('form.project.trustTitle')}
		/>
	</div>
</FormSub>

<style>
	.ed {
		display: grid;
		grid-template-columns: minmax(0, 2fr) minmax(0, 1fr) minmax(0, 1fr);
		gap: 12px 16px;
		max-width: 620px;
	}
	.toggles {
		gap: 18px;
		margin-top: 6px;
	}
	@media (max-width: 1000px) {
		.ed {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
