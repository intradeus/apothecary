// Code rules. Style rules are stylelint's (stylelint.config.mjs), the token and
// layer contracts are scripts/ (`npm run lint` runs everything). The standards
// these enforce: docs/frontend-standards.md.
import js from '@eslint/js';
import prettier from 'eslint-config-prettier';
import svelte from 'eslint-plugin-svelte';
import globals from 'globals';
import ts from 'typescript-eslint';
import svelteConfig from './svelte.config.js';

export default ts.config(
	{ ignores: ['build/', '.svelte-kit/', 'node_modules/', 'src/lib/api/generated/'] },
	js.configs.recommended,
	...ts.configs.recommended,
	...svelte.configs.recommended,
	prettier,
	...svelte.configs.prettier,
	{ languageOptions: { globals: { ...globals.browser, ...globals.node } } },
	{
		files: ['**/*.svelte', '**/*.svelte.ts'],
		languageOptions: {
			parserOptions: { parser: ts.parser, extraFileExtensions: ['.svelte'], svelteConfig }
		}
	},
	{
		rules: {
			'@typescript-eslint/no-explicit-any': 'error',
			'@typescript-eslint/no-unused-vars': [
				'error',
				{
					argsIgnorePattern: '^_',
					varsIgnorePattern: '^_',
					caughtErrors: 'none',
					ignoreRestSiblings: true
				}
			],
			'@typescript-eslint/consistent-type-imports': 'error',
			eqeqeq: ['error', 'always', { null: 'ignore' }],
			'no-alert': 'error', // confirmDialog() and toast(), never the browser's
			'no-console': ['error', { allow: ['warn', 'error'] }],
			'no-var': 'error',
			// One rule per concern: {@html} only ever meets an escaper. th() escapes
			// its params, mdToHtml() sanitizes; anything else is an XSS waiting.
			'svelte/no-at-html-tags': 'off',
			'no-restricted-syntax': [
				'error',
				{
					selector: 'SvelteMustacheTag[kind="raw"]:not([expression.callee.name=/^(th|mdToHtml)$/])',
					message: '{@html} takes th() or mdToHtml() directly, never a variable.'
				}
			],
			// A Map built and read inside one $derived is not state.
			'svelte/prefer-svelte-reactivity': 'off',
			// to.* builds every in-app URL, so the resolve() these want is already applied.
			'svelte/no-navigation-without-resolve': 'off'
		}
	},
	{
		// $props() destructures into `let` by design, and `{' · '}` keeps its spaces.
		files: ['**/*.svelte'],
		rules: {
			'no-useless-assignment': 'off', // doesn't see <script module> state
			'svelte/no-useless-mustaches': 'off',
			'svelte/prefer-const': ['error', { excludedRunes: ['$props', '$state'] }]
		}
	},
	{
		files: ['**/*.ts', '**/*.js', '**/*.mjs'],
		rules: { 'prefer-const': 'error' }
	},
	{
		// Node, not the browser: plain scripts with no TypeScript to lean on.
		files: ['scripts/**', '*.config.*'],
		rules: { 'no-console': 'off' }
	}
);
