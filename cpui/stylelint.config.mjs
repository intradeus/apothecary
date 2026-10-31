// Style rules with no project knowledge: a literal where a token belongs. The
// rules that need to know the tokens (palette privacy, raw colours, unknown
// names) are scripts/check-tokens.mjs. Layout px (paddings, gaps, a grid's
// widths) stay free: there is no spacing scale to enforce.
const literal = /^-?[\d.]/;

export default {
	overrides: [{ files: ['**/*.svelte'], customSyntax: 'postcss-html' }],
	ignoreFiles: ['build/**', '.svelte-kit/**', 'src/lib/api/generated/**'],
	rules: {
		'block-no-empty': true,
		'color-named': 'never',
		'color-no-invalid-hex': true,
		'custom-property-pattern': [
			'^[a-z][a-z0-9]*(-[a-z0-9]+)*$',
			{ message: 'Custom properties are kebab-case' }
		],
		'declaration-block-no-duplicate-properties': [
			true,
			{ ignore: ['consecutive-duplicates-with-different-values'] }
		],
		'declaration-no-important': true,
		'declaration-property-value-disallowed-list': [
			{
				'font-size': [literal],
				'font-family': [/^(?!var\(|inherit|monospace)/],
				'border-radius': [/^[\d.]+px/],
				'z-index': [/^-?[2-9]\d*$|^\d{2,}$/],
				'transition-duration': [literal],
				'animation-duration': [literal],
				transition: [/(^|\s)(?!0s)[\d.]+m?s\b/],
				animation: [/(^|\s)(?!0s)[\d.]+m?s\b/]
			},
			{ message: (prop) => `${prop}: use a token (tokens.css)` }
		],
		'font-family-no-missing-generic-family-keyword': true,
		'function-no-unknown': [true, { ignoreFunctions: ['color-mix', 'linear', 'cubic-bezier'] }],
		'no-duplicate-selectors': true,
		'no-invalid-double-slash-comments': true,
		'property-no-unknown': true,
		'selector-class-pattern': [
			// svelte-flow__* and in_progress are Svelte Flow's and the engine's words
			'^([a-z][a-z0-9]*(-[a-z0-9]+)*|svelte-flow__[a-z-]+|in_progress)$',
			{ message: 'Classes are kebab-case', resolveNestedSelectors: true }
		],
		'selector-max-id': 0,
		'selector-pseudo-class-no-unknown': [true, { ignorePseudoClasses: ['global'] }],
		'selector-pseudo-element-no-unknown': true,
		'selector-type-no-unknown': [true, { ignore: ['custom-elements'] }],
		'unit-no-unknown': true
	}
};
