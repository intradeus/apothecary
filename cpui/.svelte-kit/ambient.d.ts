
// this file is generated — do not edit it


/// <reference types="@sveltejs/kit" />

/**
 * This module provides access to environment variables that are injected _statically_ into your bundle at build time and are limited to _private_ access.
 * 
 * |         | Runtime                                                                    | Build time                                                               |
 * | ------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
 * | Private | [`$env/dynamic/private`](https://svelte.dev/docs/kit/$env-dynamic-private) | [`$env/static/private`](https://svelte.dev/docs/kit/$env-static-private) |
 * | Public  | [`$env/dynamic/public`](https://svelte.dev/docs/kit/$env-dynamic-public)   | [`$env/static/public`](https://svelte.dev/docs/kit/$env-static-public)   |
 * 
 * Static environment variables are [loaded by Vite](https://vitejs.dev/guide/env-and-mode.html#env-files) from `.env` files and `process.env` at build time and then statically injected into your bundle at build time, enabling optimisations like dead code elimination.
 * 
 * **_Private_ access:**
 * 
 * - This module cannot be imported into client-side code
 * - This module only includes variables that _do not_ begin with [`config.kit.env.publicPrefix`](https://svelte.dev/docs/kit/configuration#env) _and do_ start with [`config.kit.env.privatePrefix`](https://svelte.dev/docs/kit/configuration#env) (if configured)
 * 
 * For example, given the following build time environment:
 * 
 * ```env
 * ENVIRONMENT=production
 * PUBLIC_BASE_URL=http://site.com
 * ```
 * 
 * With the default `publicPrefix` and `privatePrefix`:
 * 
 * ```ts
 * import { ENVIRONMENT, PUBLIC_BASE_URL } from '$env/static/private';
 * 
 * console.log(ENVIRONMENT); // => "production"
 * console.log(PUBLIC_BASE_URL); // => throws error during build
 * ```
 * 
 * The above values will be the same _even if_ different values for `ENVIRONMENT` or `PUBLIC_BASE_URL` are set at runtime, as they are statically replaced in your code with their build time values.
 */
declare module '$env/static/private' {
	export const NODE_ENV: string;
	export const FNM_LOGLEVEL: string;
	export const npm_node_execpath: string;
	export const GIT_CONFIG_KEY_0: string;
	export const FNM_MULTISHELL_PATH: string;
	export const FNM_DIR: string;
	export const APPLICATION_INSIGHTS_NO_STATSBEAT: string;
	export const VSCODE_NLS_CONFIG: string;
	export const PATH: string;
	export const BROWSER: string;
	export const npm_config_noproxy: string;
	export const CLAUDE_CODE_EXECPATH: string;
	export const GDK_BACKEND: string;
	export const ELECTRON_NO_ATTACH_CONSOLE: string;
	export const MCP_CONNECTION_NONBLOCKING: string;
	export const LC_TIME: string;
	export const XDG_RUNTIME_DIR: string;
	export const FC_FONTATIONS: string;
	export const GTK3_MODULES: string;
	export const npm_execpath: string;
	export const npm_config_user_agent: string;
	export const VSCODE_HANDLES_UNCAUGHT_ERRORS: string;
	export const XDG_SESSION_ID: string;
	export const XDG_VTNR: string;
	export const VSCODE_CWD: string;
	export const SVELTEKIT_FORK: string;
	export const GIT_CONFIG_COUNT: string;
	export const GIT_EDITOR: string;
	export const SHLVL: string;
	export const VSCODE_PID: string;
	export const CUDA_PATH: string;
	export const USER: string;
	export const LC_MEASUREMENT: string;
	export const npm_package_name: string;
	export const LC_IDENTIFICATION: string;
	export const CLAUDE_CODE_ENTRYPOINT: string;
	export const PWD: string;
	export const GTK_MODULES: string;
	export const NVCC_CCBIN: string;
	export const GIT_CONFIG_VALUE_0: string;
	export const CLAUDE_CODE_ENABLE_TASKS: string;
	export const npm_lifecycle_event: string;
	export const LC_NAME: string;
	export const CLAUDE_CODE_CHILD_SESSION: string;
	export const CLAUDE_CODE_SESSION_ID: string;
	export const QT_QPA_PLATFORMTHEME: string;
	export const MAIL: string;
	export const LC_MONETARY: string;
	export const COLOR: string;
	export const DBUS_SESSION_BUS_ADDRESS: string;
	export const npm_package_json: string;
	export const VSCODE_CRASH_REPORTER_PROCESS_TYPE: string;
	export const ELECTRON_RUN_AS_NODE: string;
	export const XDG_SESSION_PATH: string;
	export const npm_config_init_module: string;
	export const DESKTOP_SESSION: string;
	export const CLAUDE_EFFORT: string;
	export const LOGNAME: string;
	export const npm_command: string;
	export const npm_config_npm_version: string;
	export const FNM_RESOLVE_ENGINES: string;
	export const CLAUDE_PID: string;
	export const LC_ADDRESS: string;
	export const EDITOR: string;
	export const npm_config_userconfig: string;
	export const COREPACK_ENABLE_AUTO_PIN: string;
	export const NO_AT_BRIDGE: string;
	export const npm_config_node_gyp: string;
	export const SHELL: string;
	export const npm_config_global_prefix: string;
	export const VSCODE_ESM_ENTRYPOINT: string;
	export const XDG_SESSION_CLASS: string;
	export const NODE: string;
	export const npm_config_local_prefix: string;
	export const XAUTHORITY: string;
	export const FNM_VERSION_FILE_STRATEGY: string;
	export const npm_config_prefix: string;
	export const CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING: string;
	export const AI_AGENT: string;
	export const npm_config_cache: string;
	export const PNPM_HOME: string;
	export const npm_config_globalconfig: string;
	export const FNM_COREPACK_ENABLED: string;
	export const XDG_SESSION_TYPE: string;
	export const COPILOT_OTEL_FILE_EXPORTER_PATH: string;
	export const VSCODE_CODE_CACHE_PATH: string;
	export const LC_NUMERIC: string;
	export const NoDefaultCurrentDirectoryInExePath: string;
	export const OLDPWD: string;
	export const TERMINAL: string;
	export const DISPLAY: string;
	export const VSCODE_IPC_HOOK: string;
	export const CLAUDECODE: string;
	export const XDG_SEAT: string;
	export const npm_package_version: string;
	export const npm_lifecycle_script: string;
	export const MOTD_SHOWN: string;
	export const HOME: string;
	export const FNM_NODE_DIST_MIRROR: string;
	export const CLAUDE_AGENT_SDK_VERSION: string;
	export const XDG_SEAT_PATH: string;
	export const LANG: string;
	export const MXC_BIN_DIR: string;
	export const _: string;
	export const XDG_CURRENT_DESKTOP: string;
	export const VSCODE_CLI: string;
	export const LC_TELEPHONE: string;
	export const FNM_ARCH: string;
	export const LC_PAPER: string;
	export const VSCODE_L10N_BUNDLE_LOCATION: string;
	export const DEBUGINFOD_URLS: string;
	export const INIT_CWD: string;
	export const CHROME_DESKTOP: string;
}

/**
 * This module provides access to environment variables that are injected _statically_ into your bundle at build time and are _publicly_ accessible.
 * 
 * |         | Runtime                                                                    | Build time                                                               |
 * | ------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
 * | Private | [`$env/dynamic/private`](https://svelte.dev/docs/kit/$env-dynamic-private) | [`$env/static/private`](https://svelte.dev/docs/kit/$env-static-private) |
 * | Public  | [`$env/dynamic/public`](https://svelte.dev/docs/kit/$env-dynamic-public)   | [`$env/static/public`](https://svelte.dev/docs/kit/$env-static-public)   |
 * 
 * Static environment variables are [loaded by Vite](https://vitejs.dev/guide/env-and-mode.html#env-files) from `.env` files and `process.env` at build time and then statically injected into your bundle at build time, enabling optimisations like dead code elimination.
 * 
 * **_Public_ access:**
 * 
 * - This module _can_ be imported into client-side code
 * - **Only** variables that begin with [`config.kit.env.publicPrefix`](https://svelte.dev/docs/kit/configuration#env) (which defaults to `PUBLIC_`) are included
 * 
 * For example, given the following build time environment:
 * 
 * ```env
 * ENVIRONMENT=production
 * PUBLIC_BASE_URL=http://site.com
 * ```
 * 
 * With the default `publicPrefix` and `privatePrefix`:
 * 
 * ```ts
 * import { ENVIRONMENT, PUBLIC_BASE_URL } from '$env/static/public';
 * 
 * console.log(ENVIRONMENT); // => throws error during build
 * console.log(PUBLIC_BASE_URL); // => "http://site.com"
 * ```
 * 
 * The above values will be the same _even if_ different values for `ENVIRONMENT` or `PUBLIC_BASE_URL` are set at runtime, as they are statically replaced in your code with their build time values.
 */
declare module '$env/static/public' {
	
}

/**
 * This module provides access to environment variables set _dynamically_ at runtime and that are limited to _private_ access.
 * 
 * |         | Runtime                                                                    | Build time                                                               |
 * | ------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
 * | Private | [`$env/dynamic/private`](https://svelte.dev/docs/kit/$env-dynamic-private) | [`$env/static/private`](https://svelte.dev/docs/kit/$env-static-private) |
 * | Public  | [`$env/dynamic/public`](https://svelte.dev/docs/kit/$env-dynamic-public)   | [`$env/static/public`](https://svelte.dev/docs/kit/$env-static-public)   |
 * 
 * Dynamic environment variables are defined by the platform you're running on. For example if you're using [`adapter-node`](https://github.com/sveltejs/kit/tree/main/packages/adapter-node) (or running [`vite preview`](https://svelte.dev/docs/kit/cli)), this is equivalent to `process.env`.
 * 
 * **_Private_ access:**
 * 
 * - This module cannot be imported into client-side code
 * - This module includes variables that _do not_ begin with [`config.kit.env.publicPrefix`](https://svelte.dev/docs/kit/configuration#env) _and do_ start with [`config.kit.env.privatePrefix`](https://svelte.dev/docs/kit/configuration#env) (if configured)
 * 
 * > [!NOTE] In `dev`, `$env/dynamic` includes environment variables from `.env`. In `prod`, this behavior will depend on your adapter.
 * 
 * > [!NOTE] To get correct types, environment variables referenced in your code should be declared (for example in an `.env` file), even if they don't have a value until the app is deployed:
 * >
 * > ```env
 * > MY_FEATURE_FLAG=
 * > ```
 * >
 * > You can override `.env` values from the command line like so:
 * >
 * > ```sh
 * > MY_FEATURE_FLAG="enabled" npm run dev
 * > ```
 * 
 * For example, given the following runtime environment:
 * 
 * ```env
 * ENVIRONMENT=production
 * PUBLIC_BASE_URL=http://site.com
 * ```
 * 
 * With the default `publicPrefix` and `privatePrefix`:
 * 
 * ```ts
 * import { env } from '$env/dynamic/private';
 * 
 * console.log(env.ENVIRONMENT); // => "production"
 * console.log(env.PUBLIC_BASE_URL); // => undefined
 * ```
 */
declare module '$env/dynamic/private' {
	export const env: {
		NODE_ENV: string;
		FNM_LOGLEVEL: string;
		npm_node_execpath: string;
		GIT_CONFIG_KEY_0: string;
		FNM_MULTISHELL_PATH: string;
		FNM_DIR: string;
		APPLICATION_INSIGHTS_NO_STATSBEAT: string;
		VSCODE_NLS_CONFIG: string;
		PATH: string;
		BROWSER: string;
		npm_config_noproxy: string;
		CLAUDE_CODE_EXECPATH: string;
		GDK_BACKEND: string;
		ELECTRON_NO_ATTACH_CONSOLE: string;
		MCP_CONNECTION_NONBLOCKING: string;
		LC_TIME: string;
		XDG_RUNTIME_DIR: string;
		FC_FONTATIONS: string;
		GTK3_MODULES: string;
		npm_execpath: string;
		npm_config_user_agent: string;
		VSCODE_HANDLES_UNCAUGHT_ERRORS: string;
		XDG_SESSION_ID: string;
		XDG_VTNR: string;
		VSCODE_CWD: string;
		SVELTEKIT_FORK: string;
		GIT_CONFIG_COUNT: string;
		GIT_EDITOR: string;
		SHLVL: string;
		VSCODE_PID: string;
		CUDA_PATH: string;
		USER: string;
		LC_MEASUREMENT: string;
		npm_package_name: string;
		LC_IDENTIFICATION: string;
		CLAUDE_CODE_ENTRYPOINT: string;
		PWD: string;
		GTK_MODULES: string;
		NVCC_CCBIN: string;
		GIT_CONFIG_VALUE_0: string;
		CLAUDE_CODE_ENABLE_TASKS: string;
		npm_lifecycle_event: string;
		LC_NAME: string;
		CLAUDE_CODE_CHILD_SESSION: string;
		CLAUDE_CODE_SESSION_ID: string;
		QT_QPA_PLATFORMTHEME: string;
		MAIL: string;
		LC_MONETARY: string;
		COLOR: string;
		DBUS_SESSION_BUS_ADDRESS: string;
		npm_package_json: string;
		VSCODE_CRASH_REPORTER_PROCESS_TYPE: string;
		ELECTRON_RUN_AS_NODE: string;
		XDG_SESSION_PATH: string;
		npm_config_init_module: string;
		DESKTOP_SESSION: string;
		CLAUDE_EFFORT: string;
		LOGNAME: string;
		npm_command: string;
		npm_config_npm_version: string;
		FNM_RESOLVE_ENGINES: string;
		CLAUDE_PID: string;
		LC_ADDRESS: string;
		EDITOR: string;
		npm_config_userconfig: string;
		COREPACK_ENABLE_AUTO_PIN: string;
		NO_AT_BRIDGE: string;
		npm_config_node_gyp: string;
		SHELL: string;
		npm_config_global_prefix: string;
		VSCODE_ESM_ENTRYPOINT: string;
		XDG_SESSION_CLASS: string;
		NODE: string;
		npm_config_local_prefix: string;
		XAUTHORITY: string;
		FNM_VERSION_FILE_STRATEGY: string;
		npm_config_prefix: string;
		CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING: string;
		AI_AGENT: string;
		npm_config_cache: string;
		PNPM_HOME: string;
		npm_config_globalconfig: string;
		FNM_COREPACK_ENABLED: string;
		XDG_SESSION_TYPE: string;
		COPILOT_OTEL_FILE_EXPORTER_PATH: string;
		VSCODE_CODE_CACHE_PATH: string;
		LC_NUMERIC: string;
		NoDefaultCurrentDirectoryInExePath: string;
		OLDPWD: string;
		TERMINAL: string;
		DISPLAY: string;
		VSCODE_IPC_HOOK: string;
		CLAUDECODE: string;
		XDG_SEAT: string;
		npm_package_version: string;
		npm_lifecycle_script: string;
		MOTD_SHOWN: string;
		HOME: string;
		FNM_NODE_DIST_MIRROR: string;
		CLAUDE_AGENT_SDK_VERSION: string;
		XDG_SEAT_PATH: string;
		LANG: string;
		MXC_BIN_DIR: string;
		_: string;
		XDG_CURRENT_DESKTOP: string;
		VSCODE_CLI: string;
		LC_TELEPHONE: string;
		FNM_ARCH: string;
		LC_PAPER: string;
		VSCODE_L10N_BUNDLE_LOCATION: string;
		DEBUGINFOD_URLS: string;
		INIT_CWD: string;
		CHROME_DESKTOP: string;
		[key: `PUBLIC_${string}`]: undefined;
		[key: `${string}`]: string | undefined;
	}
}

/**
 * This module provides access to environment variables set _dynamically_ at runtime and that are _publicly_ accessible.
 * 
 * |         | Runtime                                                                    | Build time                                                               |
 * | ------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
 * | Private | [`$env/dynamic/private`](https://svelte.dev/docs/kit/$env-dynamic-private) | [`$env/static/private`](https://svelte.dev/docs/kit/$env-static-private) |
 * | Public  | [`$env/dynamic/public`](https://svelte.dev/docs/kit/$env-dynamic-public)   | [`$env/static/public`](https://svelte.dev/docs/kit/$env-static-public)   |
 * 
 * Dynamic environment variables are defined by the platform you're running on. For example if you're using [`adapter-node`](https://github.com/sveltejs/kit/tree/main/packages/adapter-node) (or running [`vite preview`](https://svelte.dev/docs/kit/cli)), this is equivalent to `process.env`.
 * 
 * **_Public_ access:**
 * 
 * - This module _can_ be imported into client-side code
 * - **Only** variables that begin with [`config.kit.env.publicPrefix`](https://svelte.dev/docs/kit/configuration#env) (which defaults to `PUBLIC_`) are included
 * 
 * > [!NOTE] In `dev`, `$env/dynamic` includes environment variables from `.env`. In `prod`, this behavior will depend on your adapter.
 * 
 * > [!NOTE] To get correct types, environment variables referenced in your code should be declared (for example in an `.env` file), even if they don't have a value until the app is deployed:
 * >
 * > ```env
 * > MY_FEATURE_FLAG=
 * > ```
 * >
 * > You can override `.env` values from the command line like so:
 * >
 * > ```sh
 * > MY_FEATURE_FLAG="enabled" npm run dev
 * > ```
 * 
 * For example, given the following runtime environment:
 * 
 * ```env
 * ENVIRONMENT=production
 * PUBLIC_BASE_URL=http://example.com
 * ```
 * 
 * With the default `publicPrefix` and `privatePrefix`:
 * 
 * ```ts
 * import { env } from '$env/dynamic/public';
 * console.log(env.ENVIRONMENT); // => undefined, not public
 * console.log(env.PUBLIC_BASE_URL); // => "http://example.com"
 * ```
 * 
 * ```
 * 
 * ```
 */
declare module '$env/dynamic/public' {
	export const env: {
		[key: `PUBLIC_${string}`]: string | undefined;
	}
}
