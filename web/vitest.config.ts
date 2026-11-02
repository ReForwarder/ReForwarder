import { fileURLToPath } from 'node:url'
import { mergeConfig, defineConfig as defineVitestConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// Standalone test config: does not extend the app's vite.config.ts so the
// test toolchain stays decoupled from the build toolchain (Vite 8 vs the
// vitest-bundled Vite).
export default mergeConfig(
  {
    plugins: [vue()],
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url)),
      },
    },
    test: {
      environment: 'happy-dom',
    },
  },
  defineVitestConfig({}),
)
