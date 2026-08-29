// Generates the API client from the engine's OpenAPI document.
//
// Everything under src/lib/api/generated/ is machine-written and must not be
// edited or imported outside src/lib/api/ — see src/lib/api/README.md for the
// boundary rule.
import { defineConfig } from '@hey-api/openapi-ts';

export default defineConfig({
  input: '../api/openapi.yaml',
  output: {
    path: 'src/lib/api/generated',
    format: false,
    lint: false
  },
  plugins: [
    '@hey-api/typescript',
    { name: '@hey-api/sdk', asClass: false },
    { name: '@hey-api/client-fetch', bundle: true }
  ]
});
